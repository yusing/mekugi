package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"golang.org/x/term"
)

func runWrap(routerArgs, args []string) int {
	if len(args) > 0 && args[0] == "grok" {
		routerArgs = append(slices.Clone(routerArgs), "grok")
		args = args[1:]
	} else if len(args) > 0 && args[0] == "codex" {
		args = args[1:]
	} else {
		routerArgs = append(slices.Clone(routerArgs), "third-party")
	}
	if interactiveCodexArgs(args) && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := exposeHerdrCodex(); err != nil {
			fmt.Fprintln(os.Stderr, "mekugi: Herdr agent hint:", err)
		}
	}
	signals := []os.Signal{syscall.SIGTERM}
	if len(args) > 0 && args[0] == "headless" {
		signals = append(signals, os.Interrupt)
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	code, err := wrapCodex(ctx, routerArgs, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi:", err)
	}
	return code
}

func wrapCodex(ctx context.Context, routerArgs, args []string) (code int, runErr error) {
	suppliedArgs := slices.Clone(args)
	args = expandReasoningShortcuts(args)
	headless := len(args) > 0 && args[0] == "headless"
	if headless {
		args = args[1:]
	}
	if err := validateCodexArgs(args); err != nil {
		return 2, err
	}
	appUI := !headless && interactiveCodexArgs(args) && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	var resumeThread string
	var resumeArgv []string
	if appUI || headless {
		var err error
		args, resumeThread, err = appServerArgs(args)
		if err != nil {
			return 2, err
		}
		if headless && resumeThread != "" {
			return 2, errors.New("headless runs a new thread; resume is not supported")
		}
		if appUI {
			resumeArgv = appServerResumeArgv(os.Args[0], routerArgs, suppliedArgs)
		}
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		return 1, fmt.Errorf("locate codex: %w", err)
	}
	issues := router.NewCriticalErrors()
	defer func() {
		for _, message := range issues.Pending() {
			fmt.Fprintln(os.Stderr, message)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Before handoff Ctrl-C cancels startup. Once started, Codex receives the
	// foreground group's interrupt itself; never forward it or stop its router.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	stopStartupInterrupts := watchStartupInterrupts(ctx, cancel, interrupts)
	defer stopStartupInterrupts()
	ready := make(chan router.Session, 1)
	routerDone := make(chan error, 1)
	var debugPaths []string
	defer func() {
		if len(debugPaths) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "To diagnose this session, ask an agent to run:")
		directory := filepath.Dir(debugPaths[0])
		fmt.Fprintf(os.Stderr, "  mekugi inspect-session --debug-dir %s --field diagnostic\n", shellsyntax.Quote(directory))
	}()
	go func() {
		routerDone <- router.RunSession(ctx, routerArgs, issues, func(session router.Session) {
			ready <- session
		}, func(paths []string) { debugPaths = paths })
	}()
	var session router.Session
	select {
	case err := <-routerDone:
		return 1, err
	case session = <-ready:
	}
	if session.ThirdPartyOnly {
		catalogDirectory, catalogPath, err := prepareProviderCatalog(ctx, executable, args, session)
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
		defer func() {
			if err := os.RemoveAll(catalogDirectory); err != nil {
				runErr = errors.Join(runErr, err)
				code = 1
			}
		}()
		index := slices.Index(args, "--")
		if index < 0 {
			index = len(args)
		}
		args = slices.Insert(slices.Clone(args), index, "-c", fmt.Sprintf("model_catalog_json=%q", catalogPath))
		args, err = thirdPartyDefaultArgs(args, session)
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
	}

	if session.PostCompactRecovery {
		hookExecutable, err := os.Executable()
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
		var registered bool
		args, registered = postCompactHookArgs(args, hookExecutable)
		if !registered {
			fmt.Fprintln(os.Stderr, "mekugi: explicit CLI hooks configuration retained; add the post-compact SessionStart hook to that configuration to enable recovery")
		}
	}
	cmd := exec.CommandContext(ctx, executable, codexArgs(session.BaseURL, args, session.JournalEnabled, session.SkillsManagerAvailable, !session.ThirdPartyOnly)...)
	cmd.Env = append(os.Environ(), "MEKUGI_BASE_URL="+session.BaseURL)
	if session.NativeTraceDirectory != "" {
		cmd.Env = append(cmd.Env, "CODEX_ROLLOUT_TRACE_ROOT="+session.NativeTraceDirectory)
	}
	if session.AXReadOutput != "" {
		cmd.Env = append(cmd.Env, capturer.AXReadOutputEnvironment+"="+session.AXReadOutput)
	}
	if session.FrontendDirectory != "" {
		helper := ""
		if appUI {
			helper = execTrackHelper()
		}
		cmd.Env, err = frontendShellEnvironment(cmd.Env, session.FrontendDirectory, helper)
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	stopStartupInterrupts()
	select {
	case <-interrupts:
		cancel()
	default:
	}
	var waitCodex func() error
	err = ctx.Err()
	if err == nil {
		if headless {
			if session.StartHeadless == nil {
				err = errors.New("headless requires Mekugi mode")
			} else {
				waitCodex, err = session.StartHeadless(ctx, cmd, os.Stdin, os.Stdout)
			}
		} else if appUI {
			if session.StartAppUI != nil {
				waitCodex, err = session.StartAppUI(ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv)
			} else {
				waitCodex, err = router.StartAppServerUI(ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv)
			}
		} else {
			err = cmd.Start()
			waitCodex = cmd.Wait
		}
	}
	if err != nil {
		cancel()
		return 1, errors.Join(fmt.Errorf("launch codex: %w", err), <-routerDone)
	}
	codexDone := make(chan error, 1)
	go func() { codexDone <- waitCodex() }()
	var codexErr, routerErr error
	select {
	case codexErr = <-codexDone:
		cancel()
		routerErr = <-routerDone
	case routerErr = <-routerDone:
		if routerErr == nil && ctx.Err() == nil {
			routerErr = errors.New("router stopped before codex exited")
		}
		cancel()
		codexErr = <-codexDone
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](codexErr); ok {
		if appUI || headless {
			// Native UI failures carry context (for example a lost app-server)
			// around the process status. Do not discard that diagnostic merely
			// because we can preserve the child's exit code.
			routerErr = errors.Join(codexErr, routerErr)
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), routerErr
		}
		return exitErr.ExitCode(), routerErr
	}
	if err := errors.Join(codexErr, routerErr); err != nil {
		return 1, err
	}
	return 0, nil
}

// Reasoning shortcuts are session-local Codex config overrides. Preserve option
// operands and everything after -- rather than interpreting prompt contents.
func expandReasoningShortcuts(args []string) []string {
	// Non-session subcommands can own trailing child-process arguments without
	// a -- delimiter (for example mcp add and sandbox). Leave those intact.
	if !interactiveCodexArgs(args) && (len(args) == 0 || args[0] != "headless") {
		return args
	}
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return append(result, args[i:]...)
		}
		switch arg {
		case "--low", "--medium", "--high", "--xhigh", "--max", "--ultra":
			result = append(result, "-c", fmt.Sprintf("model_reasoning_effort=%q", strings.TrimPrefix(arg, "--")))
		case "-c", "--config", "--enable", "--disable", "-i", "--image", "-m", "--model", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd", "--add-dir":
			result = append(result, arg)
			if i+1 < len(args) {
				i++
				result = append(result, args[i])
			}
		default:
			result = append(result, arg)
		}
	}
	return result
}

// execTrackHelper is the installed command-segment helper beside this
// executable, or empty when it is missing and commands run untracked.
func execTrackHelper() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	helper := filepath.Join(filepath.Dir(executable), "mekugi-exec")
	if info, err := os.Stat(helper); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return helper
}

// frontendShellEnvironment restores the frontend PATH in Codex's Bash command
// shells and, with a helper, reports each command's segments to the router.
func frontendShellEnvironment(environment []string, directory, helper string) ([]string, error) {
	// Login Bash may replace inherited PATH while reading /etc/profile. Its
	// noninteractive startup file runs afterward, including for `bash -lc`.
	previous := ""
	basePath := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "BASH_ENV="); ok {
			previous = value
		}
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			basePath = value
		}
	}
	startup := ""
	if previous != "" {
		startup = ". " + shellsyntax.Quote(previous) + "\n"
	}
	startup += "PATH=" + shellsyntax.Quote(directory) + ":\"$PATH\"; export PATH\n"
	if helper != "" {
		socket, trackDirectory := router.ExecTrackPaths(directory)
		tracker := filepath.Join(filepath.Dir(directory), "exec-track.bash")
		if err := os.WriteFile(tracker, []byte(execsegment.Tracker(helper, socket, trackDirectory)), 0o600); err != nil {
			return nil, fmt.Errorf("prepare command tracking: %w", err)
		}
		startup += execsegment.Hook(tracker)
	}
	path := filepath.Join(filepath.Dir(directory), "frontend-bash-env")
	if err := os.WriteFile(path, []byte(startup), 0o600); err != nil {
		return nil, fmt.Errorf("prepare frontend shell environment: %w", err)
	}
	return append(environment,
		"PATH="+directory+string(os.PathListSeparator)+basePath,
		"BASH_ENV="+path), nil
}

// Joining the startup receiver before launch ensures an interrupt it has already
// consumed cannot be mistaken for an active-child interrupt during handoff.
func watchStartupInterrupts(ctx context.Context, cancel context.CancelFunc, interrupts <-chan os.Signal) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
		case <-done:
		case <-interrupts:
			cancel()
		}
	}()
	return sync.OnceFunc(func() {
		close(done)
		<-stopped
	})
}

func codexArgs(baseURL string, args []string, journal, skillsManagerAvailable, openAIAuth bool) []string {
	// Keep overrides in the final command's config layer: Codex subcommands
	// can replace pre-subcommand -c settings with their own. Never cross --.
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	if journal {
		args = slices.Insert(slices.Clone(args), index, "-c", "tools.update_plan.enabled=false")
		index += 2
	}
	overrides := []string{
		"--disable", "goals",
		"-c", "features.goals=false",
		"-c", `model_provider="mekugi_wrap"`,
		"-c", fmt.Sprintf(`model_providers.mekugi_wrap={name="mekugi",base_url=%q,wire_api="responses",requires_openai_auth=%t,supports_websockets=false}`, baseURL, openAIAuth),
		"-c", `include_collaboration_mode_instructions=false`,
	}
	if skillsManagerAvailable {
		overrides = append(overrides, "-c", "skills.include_instructions=false")
	}
	return slices.Insert(slices.Clone(args), index, overrides...)
}

func validateCodexArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--oss" || arg == "--local-provider" || strings.HasPrefix(arg, "--local-provider=") {
			return errors.New("mekugi codex does not support provider-selection arguments; custom providers are not supported")
		}
		var override string
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				i++
				override = args[i]
			}
		case strings.HasPrefix(arg, "--config="):
			override = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c"):
			override = strings.TrimPrefix(arg, "-c")
		}
		key, _, _ := strings.Cut(strings.TrimPrefix(override, "="), "=")
		root, _, _ := strings.Cut(key, ".")
		root = strings.Trim(strings.TrimSpace(root), `"'`)
		if root == "model_provider" || root == "model_providers" || root == "openai_base_url" || root == "oss_provider" {
			return errors.New("mekugi codex does not support provider overrides; it overrides config.toml provider selection and uses the router's default upstream")
		}
	}
	return nil
}

// Noninteractive Codex subcommands retain their ordinary terminal output too.
func interactiveCodexArgs(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--":
			return true
		case "-h", "--help", "-V", "--version":
			return false
		case "-c", "--config", "--enable", "--disable", "-i", "--image", "-m", "--model", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd", "--add-dir":
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "headless", "exec", "e", "review", "login", "logout", "mcp", "mcp-server", "app-server", "app", "completion", "sandbox", "debug", "apply", "a", "cloud", "features", "help":
			return false
		default:
			return true // resume, fork, or the initial interactive prompt.
		}
	}
	return true
}
