// Source: apps/mekugi/cmd/mekugi/wrap.go:1:609@[ebde9ad5]
package launch

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

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	headlessclient "github.com/yusing/mekugi/apps/mekugi/internal/headless"

	"github.com/yusing/mekugi/apps/mekugi/internal/router"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
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
	options, err := launchoptions.Parse(routerArgs)
	if err != nil {
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Before handoff Ctrl-C cancels startup. Once started, Codex receives the
	// foreground group's interrupt itself; never forward it or stop its router.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	stopStartupInterrupts := watchStartupInterrupts(ctx, cancel, interrupts)
	defer stopStartupInterrupts()
	var captureDirectory string
	if options.FrontendsEnabled() {
		captureDirectory, err = history.ReplayDirectory()
		if err != nil {
			return 1, fmt.Errorf("initialize replay storage: %w", err)
		}
	}
	frontends, err := composition.Frontends(ctx, appUI && options.FrontendsEnabled(), captureDirectory)
	if err != nil {
		return 1, err
	}
	issues := router.NewCriticalErrors(frontends.Notifications)
	defer func() {
		for _, message := range issues.Notices.Pending() {
			fmt.Fprintln(os.Stderr, textsanitize.Safe(message, false))
		}
	}()
	defer func() {
		if err := frontends.Close(context.WithoutCancel(ctx)); err != nil {
			runErr = errors.Join(runErr, err)
			if code == 0 {
				code = 1
			}
		}
	}()
	ready := make(chan router.Session, 1)
	routerDone := make(chan error, 1)
	var debugPaths []string
	defer func() {
		if len(debugPaths) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "To diagnose this session, ask an agent to run:")
		directory := filepath.Dir(debugPaths[0])
		fmt.Fprintf(os.Stderr, "  mekugi inspect-session --debug-dir %s --field diagnostic\n", commandargs.Quote(directory))
	}()
	go func() {
		routerDone <- router.RunSession(frontends.Terminal, frontends.Presentation, frontends.Conversation, frontends.LivePreview, frontends.Activity, frontends.History, frontends.Preferences, frontends.Recovery, frontends.Segments, frontends.Changes, frontends.Usage, frontends.AgentMetrics, frontends.TransportDiagnostics, frontends.Diagnostics, ctx, frontends.Commands, frontends.InputProjections, frontends.Guidance, frontends.Naming, frontends.Observation, frontends.Guards, options, issues, func(session router.Session) {
			ready <- session
		}, func(paths []string) { debugPaths = paths })
	}()
	stopFailedStartup := func(err error) (int, error) {
		cancel()
		return 1, errors.Join(err, <-routerDone)
	}
	var session router.Session
	select {
	case err := <-routerDone:
		return 1, err
	case session = <-ready:
	}
	if session.ThirdPartyOnly {
		catalogDirectory, catalogPath, err := prepareProviderCatalog(ctx, executable, args, session)
		if err != nil {
			return stopFailedStartup(err)
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
			return stopFailedStartup(err)
		}
	}

	cmd := exec.CommandContext(ctx, executable, codexArgs(session.BaseURL, args, session.CodeModeEnabled, session.SkillsManagerAvailable, !session.ThirdPartyOnly)...)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, shellhook.Environment+"=")
	}), "MEKUGI_BASE_URL="+session.BaseURL)
	if session.NativeTraceDirectory != "" {
		cmd.Env = append(cmd.Env, "CODEX_ROLLOUT_TRACE_ROOT="+session.NativeTraceDirectory)
	}
	if session.AXReadOutput != "" {
		cmd.Env = append(cmd.Env, evidence.AXReadOutputEnvironment+"="+session.AXReadOutput)
	}
	guards := session.Guards
	if !appUI {
		guards = nil
	}
	guard := len(guards) != 0
	if guard && session.FrontendDirectory == "" {
		return stopFailedStartup(errors.New("Shell guard requires Mekugi mode; use --shell-guard=false to disable it"))
	}
	if session.FrontendDirectory != "" {
		helper := ""
		if appUI {
			helper = execTrackHelper()
		}
		var reportEndpoints segmenttransport.Endpoints
		if frontends.Segments.Live != nil {
			reportEndpoints = frontends.Segments.Live.Endpoints()
		}
		cmd.Env, err = frontendShellEnvironment(cmd.Env, session.FrontendDirectory, helper, guards, reportEndpoints)
		if err != nil {
			return stopFailedStartup(err)
		}
		if helper != "" {
			guardDirectory := ""
			for _, registration := range guards {
				if registration.Kind == binding.RemoteWrite {
					guardDirectory, _ = shellapproval.Paths(session.FrontendDirectory)
				}
			}
			sudoDirectory, tracker := "", ""
			for _, entry := range cmd.Env {
				if value, ok := strings.CutPrefix(entry, shellapproval.DirectoryEnvironment+"="); ok {
					sudoDirectory = value
				}
				if value, ok := strings.CutPrefix(entry, shellinstrumentation.ShTrackerEnvironment+"="); ok {
					tracker = value
				}
			}
			var hash string
			if guard {
				prior, disabled, discoverErr := shellhook.Discover(ctx, cmd)
				if discoverErr != nil {
					return stopFailedStartup(fmt.Errorf("prepare shell guard: %w", discoverErr))
				}
				var config, state string
				config, state, hash, err = shellhook.Config(helper, guardDirectory, sudoDirectory, tracker, prior, disabled)
				if err == nil {
					cmd.Args = append(cmd.Args, "-c", config, "-c", state)
					for _, key := range disabled {
						fmt.Fprintf(os.Stderr, "mekugi: shell guard replaces incompatible hook %q for this invocation\n", key)
					}
				}
			} else if tracker != "" {
				cmd.Args, hash, err = optionalTrackingHookArgs(cmd.Args, helper, tracker)
			}
			if err != nil {
				if guard {
					return stopFailedStartup(err)
				}
				fmt.Fprintln(os.Stderr, "mekugi: sh segment tracking unavailable:", err)
			} else if guard {
				cmd.Env = append(cmd.Env, shellhook.Environment+"="+hash)
			}
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
			if !session.CodeModeEnabled {
				err = errors.New("headless requires Mekugi mode")
			} else {
				waitCodex, err = headlessclient.Start(ctx, cmd, os.Stdin, os.Stdout, frontends.Notifications)
			}
		} else if appUI {
			if session.StartAppUI != nil {
				waitCodex, err = session.StartAppUI(ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv, approvals)
			} else {
				waitCodex, err = router.StartAppServerUI(frontends.Terminal, frontends.Changes, frontends.Presentation, frontends.Conversation, frontends.Observation, frontends.Activity, frontends.History, frontends.Preferences, frontends.Notifications, ctx, cmd, os.Stdin, os.Stdout, resumeThread, resumeArgv, approvals)
			}
		} else {
			err = cmd.Start()
			waitCodex = cmd.Wait
		}
	}
	if err != nil {
		return stopFailedStartup(fmt.Errorf("launch codex: %w", err))
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
// function for each absolute path to one, in Bash, so remote writes
// wait for the user's approval.
func frontendShellEnvironment(environment []string, directory, helper string, guards []binding.ShellApproval, reports segmenttransport.Endpoints) ([]string, error) {
	if len(guards) != 0 && helper == "" {
		return nil, errors.New("Shell guard requires mekugi-exec beside mekugi; install both or use --shell-guard=false")
	}
	// Login Bash may replace inherited PATH while reading /etc/profile. Its
	// noninteractive startup file runs afterward, including for `bash -lc`.
	previous := ""
	basePath := ""
	userBashEnv, userBashEnvSet := "", false
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
	}
	var inheritedGuards []string
	if userBashEnvSet && filepath.Base(previous) == "frontend-bash-env" {
		if data, err := os.ReadFile(previous); err == nil && strings.HasPrefix(string(data), frontendBashEnvMarker) {
			inheritedGuards = append(inheritedGuards, filepath.Join(filepath.Dir(previous), shellinstrumentation.VCSDirectory), shellapproval.SudoDirectory(filepath.Join(filepath.Dir(previous), "bin")))
			previous = userBashEnv
		}
	}
	paths := filepath.SplitList(basePath)
	paths = slices.DeleteFunc(paths, func(path string) bool {
		return slices.Contains(inheritedGuards, filepath.Clean(path))
	})
	basePath = strings.Join(paths, string(os.PathListSeparator))
	startup := frontendBashEnvMarker
	if previous != "" {
		startup += ". " + commandargs.Quote(previous) + "\n"
	}
	guardDirectory, sudoDirectory := "", ""
	for _, guard := range guards {
		switch guard.Kind {
		case binding.RemoteWrite:
			guardDirectory, _ = shellapproval.Paths(directory)
		case binding.Sudo:
			sudoDirectory = shellapproval.SudoDirectory(directory)
		}
	}
	shellDirectory := guardDirectory
	if shellDirectory == "" {
		shellDirectory = sudoDirectory
	}
	for _, resource := range []struct {
		directory string
		tools     []string
	}{
		{guardDirectory, commandanalysis.VCSTools},
		{sudoDirectory, []string{"sudo", shellapproval.Askpass}},
	} {
		if resource.directory == "" {
			continue
		}
		if err := os.MkdirAll(resource.directory, 0700); err != nil {
			return nil, err
		}
		tools := resource.tools
		if resource.directory == shellDirectory {
			tools = slices.Concat(tools, shellinstrumentation.Shells)
		}
		for _, tool := range tools {
			if err := os.Symlink(helper, filepath.Join(resource.directory, tool)); err != nil {
				return nil, err
			}
		}
	}
	front := directory
	if guardDirectory != "" {
		front = guardDirectory + string(os.PathListSeparator) + front
	}
	if sudoDirectory != "" {
		front += string(os.PathListSeparator) + sudoDirectory
	}
	startup += "PATH=" + commandargs.Quote(front) + ":\"$PATH\"; export PATH\n"
	if guardDirectory != "" {
		startup += shellinstrumentation.Functions(guardDirectory, shellinstrumentation.KnownPaths(basePath, commandanalysis.VCSTools), commandanalysis.VCSTools)
	}
	if sudoDirectory != "" {
		tools := []string{"sudo"}
		startup += shellinstrumentation.Functions(sudoDirectory, shellinstrumentation.KnownPaths(basePath, tools), tools)
	}

	environment = slices.DeleteFunc(slices.Clone(environment), func(entry string) bool {
		return strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "BASH_ENV=") || strings.HasPrefix(entry, userBashEnvEnvironment+"=") || strings.HasPrefix(entry, shellinstrumentation.ShTrackerEnvironment+"=") || strings.HasPrefix(entry, shellinstrumentation.Guard+"=") || strings.HasPrefix(entry, "CODEX_THREAD_ID=") || strings.HasPrefix(entry, shellapproval.DirectoryEnvironment+"=")
	})
	if sudoDirectory != "" {
		environment = append(environment, shellapproval.DirectoryEnvironment+"="+sudoDirectory)
	}
	if helper != "" && reports.Requests != "" {
		socket, trackDirectory := reports.Requests, reports.Directory
		tracker := filepath.Join(filepath.Dir(directory), "exec-track.bash")
		if err := os.WriteFile(tracker, []byte(shellinstrumentation.Tracker(helper, socket, trackDirectory)), 0o600); err != nil {
			return nil, fmt.Errorf("prepare command tracking: %w", err)
		}
		startup += shellinstrumentation.Hook(tracker)
		shTracker := filepath.Join(filepath.Dir(directory), "exec-track.sh")
		if err := os.WriteFile(shTracker, []byte(shellinstrumentation.ShTracker(helper, socket, trackDirectory)), 0o600); err != nil {
			return nil, fmt.Errorf("prepare sh command tracking: %w", err)
		}
		environment = append(environment, shellinstrumentation.ShTrackerEnvironment+"="+shTracker)
	}
	path := filepath.Join(filepath.Dir(directory), "frontend-bash-env")
	if err := os.WriteFile(path, []byte(startup), 0o600); err != nil {
		return nil, fmt.Errorf("prepare frontend shell environment: %w", err)
	}
	return append(environment,
		"PATH="+front+string(os.PathListSeparator)+basePath,
		"BASH_ENV="+path, userBashEnvEnvironment+"="+previous), nil
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

func codexArgs(baseURL string, args []string, codeMode, skillsManagerAvailable, openAIAuth bool) []string {
	if codeMode {
		args = slices.Clone(args)
		// Codex applies feature toggles after -c overrides. Normalize conflicting
		// toggles too, without interpreting prompt operands after --.
		for i := 0; i < len(args) && args[i] != "--"; i++ {
			switch args[i] {
			case "--disable":
				if i+1 < len(args) && (args[i+1] == "code_mode" || args[i+1] == "code_mode_only") {
					args[i] = "--enable"
				}
			case "--disable=code_mode", "--disable=code_mode_only":
				args[i] = strings.Replace(args[i], "--disable=", "--enable=", 1)
			}
		}
	}
	// Keep overrides in the final command's config layer: Codex subcommands
	// can replace pre-subcommand -c settings with their own. Never cross --.
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	if codeMode {
		args = slices.Insert(slices.Clone(args), index, "-c", "features.code_mode=true", "-c", "features.code_mode_only=true")
		index += 4
	}
	overrides := []string{
		"--disable", "goals",
		"-c", "features.goals=false",
	}
	if openAIAuth {
		// Keep stock capabilities and runtime-only internal metadata.
		overrides = append(overrides, "-c", `model_provider="openai"`, "-c", fmt.Sprintf(`openai_base_url=%q`, baseURL))
	} else {
		overrides = append(overrides, "-c", `model_provider="mekugi_wrap"`, "-c", fmt.Sprintf(`model_providers.mekugi_wrap={name="mekugi",base_url=%q,wire_api="responses",requires_openai_auth=false,supports_websockets=false}`, baseURL))
	}
	overrides = append(overrides, "-c", `include_collaboration_mode_instructions=false`)
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
