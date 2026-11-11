package launch

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/threadcontrol/control"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/backends/codex/binding"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/BurntSushi/toml"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

func TestServerInvocationRequiresExecutionOwners(t *testing.T) {
	for _, execution := range []*threadExecution{
		nil,
		{},
		{shared: control.New(), start: &sessioncontrol.PreparedThread{}},
	} {
		code, err := runOwnedInvocation(t.Context(), nil, nil, sessionapi.NewLive(), t.TempDir(), execution)
		if code != 1 || err == nil {
			t.Fatalf("incomplete server owners admitted: code=%d error=%v", code, err)
		}
	}
}

func TestCodexArgsForcesExecutionFeatureToggles(t *testing.T) {
	// Codex folds feature toggles after -c overrides, with disables last.
	// Source: codex-rs/cli/src/main.rs FeatureToggles::to_overrides.
	for _, prefix := range [][]string{nil, {"exec"}, {"app-server"}, {"resume"}} {
		input := append(slices.Clone(prefix), "--disable", "code_mode", "--disable=code_mode_only", "--disable", "token_budget", "--disable", "unrelated", "--", "--disable=code_mode")
		original := slices.Clone(input)
		got := codexArgs("http://localhost/v1", input, false, nil)
		want := append(slices.Clone(prefix), "--enable", "code_mode", "--enable=code_mode_only", "--enable", "token_budget", "--disable", "unrelated")
		if !slices.Equal(got[:len(want)], want) || !slices.Equal(got[len(got)-2:], []string{"--", "--disable=code_mode"}) || !slices.Equal(input, original) {
			t.Fatalf("execution overrides changed unrelated flags, prompt or caller input: %q", got)
		}
	}
}

func TestCodexArgsPreservesArguments(t *testing.T) {
	{
		forwarded := []string{"exec", "-c", "model=\"example\"", "--", "a prompt with spaces"}
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true, nil)
		index := slices.Index(forwarded, "--")
		delimiter := slices.Index(args, "--")
		if delimiter < index || (delimiter-index)%2 != 0 || !slices.Equal(args[:index], forwarded[:index]) || !slices.Equal(args[delimiter:], forwarded[index:]) {
			t.Fatalf("forwarded arguments changed: %q", args)
		}
		var config struct {
			Features struct {
				Goals    *bool `toml:"goals"`
				Sleep    *bool `toml:"sleep_tool"`
				Exec     *bool `toml:"code_mode"`
				ExecOnly *bool `toml:"code_mode_only"`
			} `toml:"features"`
			IncludeCollaborationModeInstructions *bool `toml:"include_collaboration_mode_instructions"`
			Skills                               struct {
				IncludeInstructions *bool `toml:"include_instructions"`
			} `toml:"skills"`
			ModelProvider string `toml:"model_provider"`
			OpenAIBaseURL string `toml:"openai_base_url"`
			Providers     map[string]struct {
				Name       string `toml:"name"`
				BaseURL    string `toml:"base_url"`
				WireAPI    string `toml:"wire_api"`
				WebSockets bool   `toml:"supports_websockets"`
				Auth       bool   `toml:"requires_openai_auth"`
			} `toml:"model_providers"`
		}
		var settings []string
		disabled := map[string]bool{}
		for i := index; i < delimiter; i += 2 {
			if args[i] == "--disable" {
				if args[i+1] != "goals" && args[i+1] != "sleep_tool" && args[i+1] != "retain_client_tool_outputs" {
					t.Fatalf("unexpected disabled feature: %q", args)
				}
				disabled[args[i+1]] = true
				continue
			}
			if args[i] != "-c" {
				t.Fatalf("not a config override: %q", args)
			}
			settings = append(settings, args[i+1])
		}
		if !disabled["goals"] || !disabled["sleep_tool"] || !disabled["retain_client_tool_outputs"] {
			t.Fatalf("host feature toggles not disabled: %q", args)
		}
		if _, err := toml.Decode(strings.Join(settings, "\n"), &config); err != nil {
			t.Fatal(err)
		}
		if config.IncludeCollaborationModeInstructions == nil || *config.IncludeCollaborationModeInstructions {
			t.Fatalf("collaboration mode instructions not disabled: %q", args)
		}
		if config.Skills.IncludeInstructions == nil || *config.Skills.IncludeInstructions {
			t.Fatalf("skill instructions not disabled: %q", args)
		}
		if config.Features.Exec == nil || !*config.Features.Exec || config.Features.ExecOnly == nil || !*config.Features.ExecOnly {
			t.Fatalf("exec interface not forced: %q", args)
		}
		if config.Features.Goals == nil || *config.Features.Goals {
			t.Fatalf("goals not disabled: %q", args)
		}
		if config.Features.Sleep == nil || *config.Features.Sleep {
			t.Fatalf("sleep tool not disabled: %q", args)
		}
		{
			if config.ModelProvider != "openai" || config.OpenAIBaseURL != "http://127.0.0.1:12345/v1" || len(config.Providers) != 0 {
				t.Fatalf("built-in provider changed: %+v", config)
			}

		}
		withoutDelimiter := []string{"exec", "-c", `model="example"`, "prompt"}
		if got := codexArgs("http://127.0.0.1:12345/v1", withoutDelimiter, true, nil); !slices.Equal(got[:len(withoutDelimiter)], withoutDelimiter) {
			t.Fatalf("ordinary -c or prompt moved: %q", got)
		}
	}
}

// Discover installed native shells without live session guards.
func frontendFixtureShell(name string) (string, error) {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Base(directory) == shellinstrumentation.VCSDirectory {
			continue
		}
		candidate, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if path, err := exec.LookPath(candidate); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(resolved) == "mise" {
				continue
			}
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func TestFrontendPathSurvivesLoginBash(t *testing.T) {
	bash, err := frontendFixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot with spaces")
	frontend := filepath.Join(snapshot, "bin")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "mcat"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(snapshot, "user's [tools]")
	if err := os.Mkdir(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "mekugi"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	prior := filepath.Join(snapshot, "prior-bash-env")
	// Model a profile replacing PATH once, then source startup again to check
	// that restoring inherited entries does not keep growing the path.
	if err := os.WriteFile(prior, []byte("if [ -z \"${MEKUGI_PRIOR_BASH_ENV-}\" ]; then PATH=/usr/bin:/bin; fi\nexport MEKUGI_PRIOR_BASH_ENV=preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + installed + ":/usr/bin:/bin", "BASH_ENV=" + prior}, frontend, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"-c", "-lc"} {
		command := exec.Command(bash, mode, "command -v mcat; mekugi inspect-session --help; . \"$BASH_ENV\"; printf '%s\\n%s' \"$MEKUGI_PRIOR_BASH_ENV\" \"$PATH\"")
		command.Env = environment
		output, err := command.Output()
		prefix := filepath.Join(frontend, "mcat") + "\ninspect-session --help\npreserved\n"
		path, ok := strings.CutPrefix(string(output), prefix)
		if err != nil || !ok || strings.Count(":"+path+":", ":"+installed+":") != 1 {
			t.Fatalf("Bash %s frontend = %q, %v", mode, output, err)
		}
	}
}

func TestFrontendShellEnvironmentGuardsBash(t *testing.T) {
	frontend := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "mekugi-exec")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=/usr/bin:/bin"}, frontend, helper, testShellGuards(true), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := shellapproval.Paths(frontend)
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if !strings.HasPrefix(values["PATH"], guard+":"+frontend+":") {
		t.Fatalf("PATH = %q", values["PATH"])
	}
	bashEnv, err := os.ReadFile(values["BASH_ENV"])
	if err != nil || !strings.Contains(string(bashEnv), "__mekugi_vcs_paths") {
		t.Fatalf("BASH_ENV = %q, %v", bashEnv, err)
	}
}

func TestFrontendScriptSudo(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(fmt.Sprint(guard), func(t *testing.T) {
			root := t.TempDir()
			frontend, real, custom := filepath.Join(root, "bin"), filepath.Join(root, "real"), filepath.Join(root, "custom")
			for _, directory := range []string{frontend, real, custom} {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			helper := filepath.Join(root, "mekugi-exec")
			prior := filepath.Join(root, "user-bash-env")
			for path, content := range map[string]string{
				helper:                        "#!/bin/sh\ncase ${0##*/} in sudo|git) printf '%s <%s>\\n' \"$MEKUGI_VCS_GUARD_REAL\" \"$1\";; esac\n",
				filepath.Join(real, "sudo"):   "#!/bin/sh\necho BYPASSED\n",
				filepath.Join(real, "git"):    "#!/bin/sh\necho BYPASSED\n",
				filepath.Join(custom, "sudo"): "#!/bin/sh\necho BYPASSED\n",
				prior:                         filepath.Join(custom, "sudo") + "() { printf 'CUSTOM\\n'; }\n",
			} {
				if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			environment, err := frontendShellEnvironment([]string{"PATH=" + real + ":" + custom + ":/usr/bin:/bin", "BASH_ENV=" + prior}, frontend, helper, testShellGuards(guard), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "script")
			body := "#!/bin/bash\n. \"$BASH_ENV\"\nsudo 'bare argument'\n" + commandargs.Quote(filepath.Join(real, "sudo")) + " 'two words'\n" + commandargs.Quote(filepath.Join(real, "git")) + " push\n" + commandargs.Quote(filepath.Join(custom, "sudo")) + "\nexit 7\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"/bin/bash", script}, {script}} {
				cmd := exec.Command(args[0], args[1:]...)
				cmd.Env = environment
				out, err := cmd.CombinedOutput()
				want := "BYPASSED\nBYPASSED\nBYPASSED\n"
				if guard {
					want = " <bare argument>\n" + filepath.Join(real, "sudo") + " <two words>\n" + filepath.Join(real, "git") + " <push>\n"
				}
				want += "CUSTOM\n"
				if err == nil || cmd.ProcessState.ExitCode() != 7 || string(out) != want {
					t.Fatalf("script=%v output=%q err=%v; want %q and exit 7", args, out, err, want)
				}
			}
		})
	}
}

func TestCodexArgsEnforcesCollaborationModeInstructions(t *testing.T) {
	for _, forwarded := range [][]string{
		{"-c", "include_collaboration_mode_instructions=true"},
		{"-c", "include_collaboration_mode_instructions=true", "exec", "--config=include_collaboration_mode_instructions=true", "prompt"},
		{"resume", "session", "-cinclude_collaboration_mode_instructions=true", "--", "prompt"},
	} {
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true, nil)
		end := slices.Index(args, "--")
		if end < 0 {
			end = len(args)
		}
		if !slices.Equal(args[end-4:end], []string{"-c", "include_collaboration_mode_instructions=false", "-c", "skills.include_instructions=false"}) {
			t.Fatalf("enforced overrides are not last before delimiter: %q", args)
		}
	}
}

func TestCodexArgsEnforcesSkillInstructionsWhenSkillsManagerAvailable(t *testing.T) {
	for _, forwarded := range [][]string{
		{"-c", "skills.include_instructions=true"},
		{"exec", "--config=skills.include_instructions=true", "prompt"},
		{"resume", "session", "-cskills.include_instructions=true", "--", "prompt"},
	} {
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true, nil)
		end := slices.Index(args, "--")
		if end < 0 {
			end = len(args)
		}
		if !slices.Equal(args[end-2:end], []string{"-c", "skills.include_instructions=false"}) {
			t.Fatalf("enforced skill override is not last before delimiter: %q", args)
		}
	}
}

func TestCodexArgsPreservesSkillInstructionsWithoutSkillsManager(t *testing.T) {
	forwarded := []string{"exec", "--config=skills.include_instructions=true", "prompt"}
	args := codexArgs("http://127.0.0.1:12345/v1", forwarded, false, nil)
	if !slices.Equal(args[:len(forwarded)], forwarded) || slices.Contains(args[len(forwarded):], "skills.include_instructions=false") {
		t.Fatalf("skill instructions changed without skills-mgr: %q", args)
	}
}

// A fake Codex process checks the real listener before exiting, without provider traffic.
func TestWrappedCodexProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CODEX") != "1" {
		return
	}
	if slices.Contains(os.Args, "debug") && slices.Contains(os.Args, "models") {
		fmt.Fprintln(os.Stdout, testNativeModelCatalog)
		os.Exit(0)
	}
	if os.Getenv("MEKUGI_ENVIRONMENT_FACTS") != "1" || !slices.Contains(os.Args, `shell_environment_policy.set.MEKUGI_ENVIRONMENT_FACTS=""`) {
		fmt.Fprintln(os.Stderr, "invocation environment facts capability was not established and isolated")
		os.Exit(89)
	}
	interrupts := make(chan os.Signal, 1)
	if os.Getenv("MEKUGI_TEST_EXIT") == "interrupt" {
		signal.Notify(interrupts, os.Interrupt)
	}
	baseURL := os.Getenv("MEKUGI_BASE_URL")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(strings.TrimSuffix(baseURL, "/v1") + "/api/metrics")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		os.Exit(91)
	}
	// Missing required tools fails locally, producing request diagnostics without upstream traffic.
	response, err = client.Post(baseURL+"/responses", "application/json", strings.NewReader(`{"model":"test","input":"hello"}`))
	if err != nil {
		os.Exit(96)
	}
	response.Body.Close()
	if response.StatusCode < 400 {
		os.Exit(97)
	}
	fmt.Fprintln(os.Stdout, "codex stdout")
	fmt.Fprintln(os.Stderr, "codex stderr")
	if err := os.WriteFile(os.Getenv("MEKUGI_TEST_ADDRESS"), []byte(baseURL), 0o600); err != nil {
		os.Exit(92)
	}
	switch os.Getenv("MEKUGI_TEST_EXIT") {
	case "interrupt":
		<-interrupts
		if err := os.WriteFile(os.Getenv("MEKUGI_TEST_ADDRESS")+".interrupt", nil, 0o600); err != nil {
			os.Exit(94)
		}
		time.Sleep(time.Minute)
		os.Exit(95)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {}
	case "wait":
		time.Sleep(time.Minute)
		os.Exit(93)
	default:
		code, _ := strconv.Atoi(os.Getenv("MEKUGI_TEST_EXIT"))
		os.Exit(code)
	}
}

func TestWrappedRouterProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_ROUTER") != "1" {
		return
	}
	os.Args = []string{os.Args[0], "codex"}
	os.Exit(Run())
}

func TestWrapTerminalInterruptAndTermination(t *testing.T) {
	directory := t.TempDir()
	logDirectory := t.TempDir()
	t.Setenv("TMPDIR", logDirectory)
	runtimeDirectory := t.TempDir()
	addressFile := filepath.Join(directory, "address")
	t.Setenv("XAI_API_KEY", "test")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("MEKUGI_TEST_ROUTER", "1")
	t.Setenv("MEKUGI_TEST_CODEX", "1")
	t.Setenv("MEKUGI_TEST_EXIT", "interrupt")
	t.Setenv("MEKUGI_TEST_ADDRESS", addressFile)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	stub := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestWrappedCodexProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWrappedRouterProcess$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stdout, logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waitFile := func(path string) []byte {
		t.Helper()
		for {
			if data, err := os.ReadFile(path); err == nil {
				return data
			}
			select {
			case err := <-done:
				t.Fatalf("wrapper exited early: %v", err)
			case <-ctx.Done():
				t.Fatal("wrapper timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	baseURL := string(waitFile(addressFile))
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	waitFile(addressFile + ".interrupt")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(strings.TrimSuffix(baseURL, "/v1") + "/api/metrics")
	if err != nil {
		t.Fatalf("router stopped on terminal interrupt: %v", err)
	}
	response.Body.Close()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || cmd.ProcessState.ExitCode() != 143 {
		t.Fatalf("termination = %v, %v", cmd.ProcessState, err)
	}
	if strings.Contains(logs.String(), "level=") || strings.Contains(logs.String(), "router log:") {
		t.Fatal("operational logs reached Codex")
	}
	if stdout.String() != "codex stdout\n" || !strings.Contains(logs.String(), "codex stderr\n") {
		t.Fatal("Codex output was not inherited")
	}
	if strings.Contains(logs.String(), "mekugi dashboard:") {
		t.Fatalf("superseded dashboard announcement: %q", logs.String())
	}
	if entries, err := os.ReadDir(logDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected log files: %v %v", entries, err)
	}

	entries, err := os.ReadDir(runtimeDirectory)
	if err != nil || len(entries) != 0 {
		t.Errorf("runtime resources survived: %v, %v", entries, err)
	}
}

func TestWrapCodexLifecycle(t *testing.T) {
	tests := []struct {
		name, exit string
		code       int
	}{
		{"success", "0", 0}, {"failure", "23", 23}, {"signal", "signal", 143}, {"termination", "wait", 143},
	}
	if selected := os.Getenv("MEKUGI_TEST_WRAP_LIFECYCLE"); selected != "" {
		index, err := strconv.Atoi(selected)
		if err != nil || index < 0 || index >= len(tests) {
			t.Fatalf("invalid lifecycle selection %q", selected)
		}
		testWrapCodexLifecycle(t, tests[index].exit, tests[index].code)
		return
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWrapCodexLifecycle$")
			command.Env = append(os.Environ(), "MEKUGI_TEST_WRAP_LIFECYCLE="+strconv.Itoa(index))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("isolated lifecycle: %v\n%s", err, output)
			}
		})
	}
}

func testWrapCodexLifecycle(t *testing.T, exit string, wantCode int) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	runtimeDirectory := t.TempDir()
	addressFile := filepath.Join(directory, "address")
	t.Setenv("XAI_API_KEY", "test")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("MEKUGI_TEST_CODEX", "1")
	t.Setenv("MEKUGI_TEST_EXIT", exit)
	t.Setenv("MEKUGI_TEST_ADDRESS", addressFile)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	stub := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestWrappedCodexProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if exit == "wait" {
		go func() {
			for {
				if _, err := os.Stat(addressFile); err == nil {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}()
	}
	deadline := time.AfterFunc(20*time.Second, cancel)
	defer deadline.Stop()
	var routerArgs []string
	code, err := wrapCodex(ctx, routerArgs, []string{"exec", "prompt with spaces"})
	if err != nil || code != wantCode {
		t.Fatalf("wrap = %d, %v; want %d", code, err, wantCode)
	}
	address, err := os.ReadFile(addressFile)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimSuffix(strings.TrimPrefix(string(address), "http://"), "/v1")
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err == nil {
		conn.Close()
		t.Error("listener survived Codex exit")
	}
	entries, err := os.ReadDir(runtimeDirectory)
	if err != nil || len(entries) != 0 {
		t.Errorf("runtime resources survived: %v, %v", entries, err)
	}
}

func TestWrapCodexMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, err := wrapCodex(t.Context(), nil, nil)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "locate codex") {
		t.Fatalf("wrap = %d, %v", code, err)
	}
}

func TestValidateCodexArgs(t *testing.T) {
	for _, args := range [][]string{
		{"--oss"}, {"exec", "--local-provider", "ollama"}, {"--local-provider=ollama"},
		{"-c", `model_provider="other"`}, {"--config=model_providers.other={}"},
		{`-cmodel_provider="other"`}, {`-c=model_provider="other"`},
		{"--config", `"model_providers".mekugi_wrap.base_url="https://example.com"`},
		{"-c", `openai_base_url="https://example.com"`}, {"-c", `oss_provider="ollama"`},
	} {
		if err := validateCodexArgs(args, false); err == nil {
			t.Errorf("accepted provider override: %q", args)
		}
	}
	for _, args := range [][]string{
		nil, {"exec", "a prompt with spaces"}, {"--profile", "work"},
		{"exec", "-c", `model="example"`}, {"--", "--oss"},
		{"--", `-cmodel_provider="other"`}, {"-c"},
	} {
		if err := validateCodexArgs(args, false); err != nil {
			t.Errorf("rejected ordinary arguments %q: %v", args, err)
		}
	}
}

func TestRunWrapRequiresCustomProvider(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "launched")
	t.Setenv("PATH", directory)
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte("#!/bin/sh\nprintf launched > "+strconv.Quote(marker)+"\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"exec", "--yolo", "hello"}} {
		if code := runWrap(nil, args); code != 2 {
			t.Errorf("unauthenticated runWrap(%q) = %d", args, code)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unauthenticated standalone launched Codex: %v", err)
	}
}

func TestWrapCodexStartupFailures(t *testing.T) {
	for _, failure := range []string{"router", "codex"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("TMPDIR", t.TempDir())
			runtimeDirectory := t.TempDir()
			configDirectory := t.TempDir()
			t.Setenv("XAI_API_KEY", "test")
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", configDirectory)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			marker := filepath.Join(directory, "launched")
			stub := "#!/bin/sh\ntouch " + strconv.Quote(marker) + "\n"
			if failure == "codex" {
				stub = "#!/nonexistent-mekugi-test-interpreter\n"
			} else {
				statePath := filepath.Join(directory, "state-file")
				if err := os.WriteFile(statePath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("XDG_STATE_HOME", statePath)
			}
			if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			code, err := wrapCodex(ctx, nil, nil)
			if code != 1 || err == nil {
				t.Fatalf("wrap = %d, %v", code, err)
			}
			if failure == "router" && !strings.Contains(err.Error(), "initialize replay storage") {
				t.Fatalf("unexpected router startup failure: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("Codex launched despite startup failure: %v", err)
			}
			entries, err := os.ReadDir(runtimeDirectory)
			if err != nil || len(entries) != 0 {
				t.Errorf("runtime resources survived: %v, %v", entries, err)
			}
		})
	}
}

func TestWrapDebugPassesAXLogToCodex(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	directory, debugDirectory := t.TempDir(), t.TempDir()
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", debugDirectory)
	t.Setenv("MEKUGI_AX_OUTPUT", "")
	marker := filepath.Join(directory, "ax-path")
	stub := "#!/bin/sh\n" +
		"test -n \"$MEKUGI_AX_OUTPUT\" && test -f \"$MEKUGI_AX_OUTPUT\" || exit 93\n" +
		"printf '%s' \"$MEKUGI_AX_OUTPUT\" > " + strconv.Quote(marker) + "\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	code, err := wrapCodex(t.Context(), []string{"--debug"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("debug wrap = %d, %v", code, err)
	}
	path, err := os.ReadFile(marker)
	if err != nil || !filepath.IsAbs(string(path)) {
		t.Fatalf("AX log was not inherited: %q, %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(string(path)), "ax.json")); err != nil {
		t.Fatalf("automatic report missing: %v", err)
	}
}

func TestStartupInterruptHandoffJoinsConsumedSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	interrupts := make(chan os.Signal)
	stop := watchStartupInterrupts(ctx, cancel, interrupts)
	// The rendezvous proves the startup receiver consumed this signal, but does
	// not assume it has run cancel yet. Handoff must join that pending work.
	interrupts <- os.Interrupt
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatal("handoff lost a consumed startup interrupt")
	}
	stop()
}

func TestStartupInterruptHandoffLeavesChildSignalAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	interrupts := make(chan os.Signal, 1)
	stop := watchStartupInterrupts(ctx, cancel, interrupts)
	stop()
	interrupts <- os.Interrupt
	if ctx.Err() != nil {
		t.Fatal("startup receiver canceled after handoff")
	}
}

func TestFrontendShellEnvironmentWithoutGuardKeepsTracking(t *testing.T) {
	bash, err := frontendFixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	frontend := filepath.Join(root, "bin")
	if err := os.Mkdir(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "mekugi-exec")
	log := filepath.Join(root, "tracked-script")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\" > \"$MEKUGI_TEST_TRACK_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + os.Getenv("PATH"), "MEKUGI_TEST_TRACK_LOG=" + log, "CODEX_THREAD_ID=outer-thread", shellinstrumentation.Guard + "=1", shellinstrumentation.ShTrackerEnvironment + "=/outer/exec-track.sh"}, frontend, helper, nil, segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(environment, "PATH="+frontend+":"+os.Getenv("PATH")) {
		t.Fatalf("unguarded environment = %q", environment)
	}
	if slices.Contains(environment, shellinstrumentation.Guard+"=1") || !slices.Contains(environment, shellinstrumentation.ShTrackerEnvironment+"="+filepath.Join(root, "exec-track.sh")) {
		t.Fatalf("new session inherited shell tracking state: %q", environment)
	}
	if slices.Contains(environment, "CODEX_THREAD_ID=outer-thread") {
		t.Fatal("new session inherited command identity")
	}
	guard, _ := shellapproval.Paths(frontend)
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("unguarded shell installed guard: %v", err)
	}
	if _, err := os.Stat(shellapproval.SudoDirectory(frontend)); !os.IsNotExist(err) {
		t.Fatalf("unguarded shell installed sudo helpers: %v", err)
	}
	for _, entry := range environment {
		if strings.HasPrefix(entry, shellapproval.DirectoryEnvironment+"=") {
			t.Fatal("unguarded shell exposed sudo instrumentation")
		}
	}
	const script = "printf local; printf read"
	// The host's hook shell inherits the launcher environment, not the tool's
	// thread injection. It must execute without entering command tracking.
	hook := exec.Command(bash, "-c", script)
	hook.Env = environment
	if output, err := hook.CombinedOutput(); err != nil || string(output) != "localread" {
		t.Fatalf("hook command = %q, %v", output, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("hook shell started command tracking: %v", err)
	}
	command := exec.Command(bash, "-c", script)
	command.Env = append(slices.Clone(environment), "CODEX_THREAD_ID=tool-thread")
	if output, err := command.CombinedOutput(); err != nil || string(output) != "localread" {
		t.Fatalf("unguarded command = %q, %v", output, err)
	}
	compatible := exec.Command(bash, "-c", `(( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 1) ))`)
	compatible.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := compatible.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("check Bash tracking support: %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("unsupported Bash started tracking: %v", err)
		}
		return
	}
	if tracked, err := os.ReadFile(log); err != nil || string(tracked) != script+"\n" {
		t.Fatalf("tracked script = %q, %v", tracked, err)
	}
}

func TestFrontendNestedGuardOptOut(t *testing.T) {
	root := t.TempDir()
	outer, inner, real := filepath.Join(root, "outer", "bin"), filepath.Join(root, "inner", "bin"), filepath.Join(root, "user", shellinstrumentation.VCSDirectory)
	for _, path := range []string{outer, inner, real} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	helper, userEnv := filepath.Join(root, "mekugi-exec"), filepath.Join(root, "user-bash-env")
	for path, script := range map[string]string{
		helper:                     "#!/bin/sh\nprintf OUTER_GUARD\n",
		filepath.Join(real, "git"): "#!/bin/sh\nprintf REAL_GIT\n",
		userEnv:                    "export USER_STARTUP=preserved\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + real + ":/usr/bin:/bin", "BASH_ENV=" + userEnv}
	env, err := frontendShellEnvironment(env, outer, helper, testShellGuards(true), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	env, err = frontendShellEnvironment(env, inner, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "PATH="+inner+":"+real+":/usr/bin:/bin") {
		t.Fatalf("nested PATH retains stale frontend: %q", env)
	}
	for _, name := range []string{"git", filepath.Join(real, "git")} {
		cmd := exec.Command("/bin/bash", "-c", commandargs.Quote(name)+" push; printf ' %s' \"$USER_STARTUP\"")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "REAL_GIT preserved" {
			t.Fatalf("nested opt-out %q = %q, %v", name, out, err)
		}
	}
}

func TestInheritedFrontendEnvironment(t *testing.T) {
	root := t.TempDir()
	startup := filepath.Join(root, "frontend-bash-env")
	for _, marked := range []bool{false, true} {
		body := "# user startup\n"
		if marked {
			body = frontendBashEnvMarker
		}
		if err := os.WriteFile(startup, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, previous := range []string{"", "/user/startup"} {
			environment := []string{"PATH=" + root + "/bin:" + root + "/vcs:" + root + "/sudo:" + root + "-neighbor/bin:/usr/bin", "BASH_ENV=" + startup, userBashEnvEnvironment + "=" + previous, "KEEP=value", "TERM=dumb", "TMUX=outer", "HERDR_ENV=1", "MEKUGI_BASE_URL=outer", "MEKUGI_RUNTIME_DIR=outer"}
			for _, key := range []string{"CODEX_THREAD_ID", shellinstrumentation.Guard, shellinstrumentation.ShTrackerEnvironment, shellapproval.DirectoryEnvironment, shellinstrumentation.ItemEnvironment, shellinstrumentation.RealEnvironment} {
				environment = append(environment, key+"=outer")
			}
			want := slices.Clone(environment[:9])
			want = slices.Delete(want, 2, 3)
			if marked {
				want[0] = "PATH=" + root + "-neighbor/bin:/usr/bin"
				want = slices.Delete(want, 1, 2)
				if previous != "" {
					want = append(want[:1], append([]string{"BASH_ENV=" + previous}, want[1:]...)...)
				}
			}
			if got := withoutInheritedFrontend(environment); !slices.Equal(got, want) {
				t.Fatalf("marked=%v previous=%q: scrubbed=%q want=%q", marked, previous, got, want)
			}
			serverWant := slices.DeleteFunc(slices.Clone(want), func(entry string) bool {
				return strings.HasPrefix(entry, "TERM=") || strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "HERDR_") || strings.HasPrefix(entry, "MEKUGI_BASE_URL=") || strings.HasPrefix(entry, "MEKUGI_RUNTIME_DIR=")
			})
			if got := serverEnvironment(environment); !slices.Equal(got, append(serverWant, "TERM=xterm-256color")) {
				t.Fatalf("server environment=%q", got)
			}
		}
	}
	for _, terminal := range []string{"", "dumb", "screen-256color"} {
		want := terminal
		if want == "" || want == "dumb" {
			want = "xterm-256color"
		}
		if got := serverEnvironment([]string{"TERM=" + terminal}); !slices.Equal(got, []string{"TERM=" + want}) {
			t.Fatalf("terminal %q: %q", terminal, got)
		}
	}
}

func testShellGuards(enabled bool) []binding.ShellApproval {
	if !enabled {
		return nil
	}
	return []binding.ShellApproval{{Kind: binding.RemoteWrite}, {Kind: binding.Sudo}}
}
