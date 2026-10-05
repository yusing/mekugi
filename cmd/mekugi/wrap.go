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
	"github.com/yusing/mekugi/internal/vcsguard"
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
	approvals := false
	if appUI || headless {
		var err error
		var yolo bool
		args, resumeThread, yolo, err = appServerArgs(args)
		if err != nil {
			return 2, err
		}
		if headless && !yolo {
			return 2, errors.New("headless cannot answer approvals; it requires explicit --yolo")
		}
		if headless && resumeThread != "" {
			return 2, errors.New("headless runs a new thread; resume is not supported")
		}
		approvals = appUI && !yolo
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
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, vcsguard.HookEnvironment+"=")
	}), "MEKUGI_BASE_URL="+session.BaseURL)
	if session.NativeTraceDirectory != "" {
		cmd.Env = append(cmd.Env, "CODEX_ROLLOUT_TRACE_ROOT="+session.NativeTraceDirectory)
	}
	if session.AXReadOutput != "" {
		cmd.Env = append(cmd.Env, capturer.AXReadOutputEnvironment+"="+session.AXReadOutput)
	}
	guard := appUI && session.VCSGuard
	if guard && session.FrontendDirectory == "" {
		cancel()
		return 1, errors.Join(errors.New("VCS guard requires Mekugi mode; use --vcs-guard=false to disable it"), <-routerDone)
	}
	if session.FrontendDirectory != "" {
		helper := ""
		if appUI {
			helper = execTrackHelper()
		}
		cmd.Env, err = frontendShellEnvironment(cmd.Env, session.FrontendDirectory, helper, guard)
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
		if guard {
			guard, _ := vcsguard.Paths(session.FrontendDirectory)
			cmd.Args, err = vcsGuardHookArgs(cmd.Args, helper, guard)
			if err != nil {
				cancel()
				return 1, errors.Join(err, <-routerDone)
			}
			cmd.Env = append(cmd.Env, vcsguard.HookEnvironment+"="+vcsguard.HookCommand(helper, guard))
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
				waitCodex, err = session.StartAppUI(ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv, approvals)
			} else {
				waitCodex, err = router.StartAppServerUI(ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv, approvals)
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
			// UI failures carry context (for example a lost app-server)
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

const userBashEnvEnvironment = "MEKUGI_USER_BASH_ENV"
const frontendBashEnvMarker = "# Mekugi frontend shell startup\n"

// frontendShellEnvironment restores the frontend PATH in Codex's Bash command
// shells and, with a helper, reports each command's segments to the router.
// A guard puts the helper ahead of the version-control tools, and defines a
// function for each absolute path to one, in Bash and zsh, so remote writes
// wait for the user's approval.
func frontendShellEnvironment(environment []string, directory, helper string, guard bool) ([]string, error) {
	// Login Bash may replace inherited PATH while reading /etc/profile. Its
	// noninteractive startup file runs afterward, including for `bash -lc`.
	previous := ""
	basePath := ""
	userBashEnv, userBashEnvSet := "", false
	zdotdir, zdotdirSet := "", false
	userZdotdir, userZdotdirSet := "", false
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "BASH_ENV="); ok {
			previous = value
		}
		if value, ok := strings.CutPrefix(entry, userBashEnvEnvironment+"="); ok {
			userBashEnv, userBashEnvSet = value, true
		}
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			basePath = value
		}
		if value, ok := strings.CutPrefix(entry, "ZDOTDIR="); ok {
			zdotdir, zdotdirSet = value, true
		}
		if value, ok := strings.CutPrefix(entry, vcsguard.UserZdotdirEnvironment+"="); ok {
			userZdotdir, userZdotdirSet = value, true
		}
	}
	var inheritedGuards []string
	if userBashEnvSet && filepath.Base(previous) == "frontend-bash-env" {
		if data, err := os.ReadFile(previous); err == nil && strings.HasPrefix(string(data), frontendBashEnvMarker) {
			inheritedGuards = append(inheritedGuards, filepath.Join(filepath.Dir(previous), vcsguard.Directory))
			previous = userBashEnv
		}
	}
	restoreZsh := zdotdirSet && vcsguard.IsZshStartup(zdotdir)
	if restoreZsh {
		inheritedGuards = append(inheritedGuards, filepath.Join(filepath.Dir(zdotdir), vcsguard.Directory))
		// A nested session: the user's files are those the outer one ran.
		zdotdir, zdotdirSet = userZdotdir, userZdotdirSet
	}
	paths := filepath.SplitList(basePath)
	paths = slices.DeleteFunc(paths, func(path string) bool {
		return slices.Contains(inheritedGuards, filepath.Clean(path))
	})
	basePath = strings.Join(paths, string(os.PathListSeparator))
	startup := frontendBashEnvMarker
	if previous != "" {
		startup += ". " + shellsyntax.Quote(previous) + "\n"
	}
	front := directory
	if guard {
		if helper == "" {
			return nil, errors.New("VCS guard requires mekugi-exec beside mekugi; install both or use --vcs-guard=false")
		}
		guardDirectory, _ := vcsguard.Paths(directory)
		if err := os.MkdirAll(guardDirectory, 0o700); err != nil {
			return nil, fmt.Errorf("prepare VCS write guard: %w", err)
		}
		for _, tool := range slices.Concat(vcsguard.Tools, vcsguard.Shells) {
			if err := os.Symlink(helper, filepath.Join(guardDirectory, tool)); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("prepare VCS write guard: %w", err)
			}
		}
		front = guardDirectory + string(os.PathListSeparator) + directory
	}
	startup += "PATH=" + shellsyntax.Quote(front) + ":\"$PATH\"; export PATH\n"
	var zsh []string
	if guard {
		guardDirectory, _ := vcsguard.Paths(directory)
		known := vcsguard.KnownPaths(basePath)
		startup += vcsguard.Functions("bash", guardDirectory, known)
		// Zsh reads no BASH_ENV; its startup files, which ZDOTDIR locates,
		// run the user's own and then the same setup.
		zshDirectory := filepath.Join(filepath.Dir(directory), "zsh")
		if err := vcsguard.WriteZshStartup(zshDirectory, vcsguard.ZshPath(guardDirectory, directory)+vcsguard.Functions("zsh", guardDirectory, known)); err != nil {
			return nil, fmt.Errorf("prepare VCS write guard: %w", err)
		}
		zsh = append(zsh, "ZDOTDIR="+zshDirectory)
		if zdotdirSet {
			zsh = append(zsh, vcsguard.UserZdotdirEnvironment+"="+zdotdir)
		}
	} else if restoreZsh && zdotdirSet {
		zsh = append(zsh, "ZDOTDIR="+zdotdir)
	}
	environment = slices.DeleteFunc(slices.Clone(environment), func(entry string) bool {
		return strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, userBashEnvEnvironment+"=") ||
			((guard || restoreZsh) && (strings.HasPrefix(entry, "ZDOTDIR=") || strings.HasPrefix(entry, vcsguard.UserZdotdirEnvironment+"=")))
	})
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
	return append(append(environment,
		"PATH="+front+string(os.PathListSeparator)+basePath,
		"BASH_ENV="+path, userBashEnvEnvironment+"="+previous), zsh...), nil
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
