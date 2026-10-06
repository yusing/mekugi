package main

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

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestCodexArgsForcesExecutionFeatureToggles(t *testing.T) {
	// Codex folds feature toggles after -c overrides, with disables last.
	// Source: codex-rs/cli/src/main.rs FeatureToggles::to_overrides.
	for _, prefix := range [][]string{nil, {"exec"}, {"app-server"}, {"resume"}} {
		input := append(slices.Clone(prefix), "--disable", "code_mode", "--disable=code_mode_only", "--disable", "unrelated", "--", "--disable=code_mode")
		original := slices.Clone(input)
		got := codexArgs("http://localhost/v1", input, true, false, true)
		want := append(slices.Clone(prefix), "--enable", "code_mode", "--enable=code_mode_only", "--disable", "unrelated")
		if !slices.Equal(got[:len(want)], want) || !slices.Equal(got[len(got)-2:], []string{"--", "--disable=code_mode"}) || !slices.Equal(input, original) {
			t.Fatalf("execution overrides changed unrelated flags, prompt or caller input: %q", got)
		}
		passthrough := codexArgs("http://localhost/v1", input, false, false, true)
		if !slices.Equal(passthrough[:len(want)], original[:len(want)]) {
			t.Fatalf("passthrough feature flags changed: %q", passthrough)
		}
	}
}

func TestCodexArgsPreservesArguments(t *testing.T) {
	forwarded := []string{"exec", "-c", "model=\"example\"", "--", "a prompt with spaces"}
	args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true, true, true)
	index := slices.Index(forwarded, "--")
	if !slices.Equal(args[:index], forwarded[:index]) || !slices.Equal(args[index+18:], forwarded[index:]) {
		t.Fatalf("forwarded arguments changed: %q", args)
	}
	var config struct {
		Features struct {
			Goals    *bool `toml:"goals"`
			Exec     *bool `toml:"code_mode"`
			ExecOnly *bool `toml:"code_mode_only"`
		} `toml:"features"`
		IncludeCollaborationModeInstructions *bool `toml:"include_collaboration_mode_instructions"`
		Skills                               struct {
			IncludeInstructions *bool `toml:"include_instructions"`
		} `toml:"skills"`
		ModelProvider string `toml:"model_provider"`
		Providers     map[string]struct {
			Name       string `toml:"name"`
			BaseURL    string `toml:"base_url"`
			WireAPI    string `toml:"wire_api"`
			WebSockets bool   `toml:"supports_websockets"`
			Auth       bool   `toml:"requires_openai_auth"`
		} `toml:"model_providers"`
	}
	if !slices.Equal(args[index+6:index+8], []string{"--disable", "goals"}) {
		t.Fatalf("goal feature toggle not disabled: %q", args)
	}
	var settings []string
	for i := index; i < index+18; i += 2 {
		if args[i] == "--disable" {
			continue
		}
		if args[i] != "-c" {
			t.Fatalf("not a config override: %q", args)
		}
		settings = append(settings, args[i+1])
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
	provider := config.Providers[config.ModelProvider]
	if provider.Name == "" || provider.BaseURL != "http://127.0.0.1:12345/v1" || provider.WireAPI != "responses" || !provider.Auth || provider.WebSockets {
		t.Fatalf("provider = %+v", provider)
	}
	withoutDelimiter := []string{"exec", "-c", `model="example"`, "prompt"}
	if got := codexArgs("http://127.0.0.1:12345/v1", withoutDelimiter, true, true, true); !slices.Equal(got[:len(withoutDelimiter)], withoutDelimiter) {
		t.Fatalf("ordinary -c or prompt moved: %q", got)
	}
}

// Discover installed native shells without live session guards.
func frontendFixtureShell(name string) (string, error) {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Base(directory) == vcsguard.Directory {
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
	prior := filepath.Join(snapshot, "prior-bash-env")
	if err := os.WriteFile(prior, []byte("export MEKUGI_PRIOR_BASH_ENV=preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + os.Getenv("PATH"), "BASH_ENV=" + prior}, frontend, "", false)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, "-lc", "command -v mcat; printf '%s' \"$MEKUGI_PRIOR_BASH_ENV\"")
	command.Env = environment
	output, err := command.Output()
	if err != nil || string(output) != filepath.Join(frontend, "mcat")+"\npreserved" {
		t.Fatalf("login Bash frontend = %q, %v", output, err)
	}
}

// With the VCS guard enabled, it comes first in Bash and zsh alike, and zsh's
// startup wrappers keep the user's ZDOTDIR for their own files.
func TestFrontendShellEnvironmentGuardsBashAndZsh(t *testing.T) {
	frontend := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "mekugi-exec")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	environment, err := frontendShellEnvironment([]string{"PATH=/usr/bin:/bin", "ZDOTDIR=" + user, vcsguard.UserZdotdirEnvironment + "=/stale"}, frontend, helper, true)
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := vcsguard.Paths(frontend)
	zdotdir := filepath.Join(filepath.Dir(frontend), "zsh")
	values := map[string][]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = append(values[key], value)
	}
	if !slices.Equal(values["ZDOTDIR"], []string{zdotdir}) || !slices.Equal(values[vcsguard.UserZdotdirEnvironment], []string{user}) {
		t.Fatalf("zsh environment = %q", environment)
	}
	if path := values["PATH"]; !strings.HasPrefix(path[len(path)-1], guard+":"+frontend+":") {
		t.Fatalf("PATH = %q", path)
	}
	bashEnv, err := os.ReadFile(values["BASH_ENV"][0])
	if err != nil || !strings.Contains(string(bashEnv), "__mekugi_vcs_paths") {
		t.Fatalf("BASH_ENV = %q, %v", bashEnv, err)
	}
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin", "mekugi-setup.zsh"} {
		if _, err := os.Stat(filepath.Join(zdotdir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A nested session keeps the user's files, not the outer session's
	// wrappers, which would otherwise source themselves.
	inner := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	nested, err := frontendShellEnvironment(environment, inner, helper, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(nested, vcsguard.UserZdotdirEnvironment+"="+user) || !slices.Contains(nested, "ZDOTDIR="+filepath.Join(filepath.Dir(inner), "zsh")) {
		t.Fatalf("nested environment = %q", nested)
	}
	if zsh, err := frontendFixtureShell("zsh"); err == nil {
		if err := os.WriteFile(filepath.Join(user, ".zshenv"), []byte("print -n user\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(zsh, "-c", "printf ' %s' ${path[1]:t}")
		command.Env = append(nested, "HOME="+t.TempDir())
		if output, err := command.Output(); err != nil || string(output) != "user "+vcsguard.Directory {
			t.Fatalf("nested zsh = %q, %v", output, err)
		}
	}
}

func TestCodexArgsEnforcesCollaborationModeInstructions(t *testing.T) {
	for _, forwarded := range [][]string{
		{"-c", "include_collaboration_mode_instructions=true"},
		{"-c", "include_collaboration_mode_instructions=true", "exec", "--config=include_collaboration_mode_instructions=true", "prompt"},
		{"resume", "session", "-cinclude_collaboration_mode_instructions=true", "--", "prompt"},
	} {
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true, true, true)
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
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, false, true, true)
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
	args := codexArgs("http://127.0.0.1:12345/v1", forwarded, false, false, true)
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
		if os.Getenv("MEKUGI_TEST_CATALOG_DELAY") == "1" {
			if err := os.WriteFile(os.Getenv("MEKUGI_TEST_ADDRESS")+".preparing", nil, 0o600); err != nil {
				os.Exit(99)
			}
			fmt.Fprintln(os.Stderr, "private bootstrap diagnostic")
			time.Sleep(time.Minute)
		}
		fmt.Fprintln(os.Stdout, testNativeModelCatalog)
		os.Exit(0)
	}
	if os.Getenv("MEKUGI_TEST_PINNED_CATALOG") == "1" {
		var catalogPath string
		for _, arg := range os.Args {
			if after, ok := strings.CutPrefix(arg, "model_catalog_json="); ok {
				catalogPath, _ = strconv.Unquote(after)
			}
		}
		body, err := os.ReadFile(catalogPath)
		if err != nil || !bytes.Contains(body, []byte("grok-4.6")) || !bytes.Contains(body, []byte("freeform")) {
			os.Exit(98)
		}
		if err := os.WriteFile(os.Getenv("MEKUGI_TEST_ADDRESS")+".catalog", []byte(catalogPath), 0o600); err != nil {
			os.Exit(99)
		}
		if os.Getenv("MEKUGI_TEST_LAUNCH_MODE") == "standalone" {
			var settings struct {
				Model     string
				Providers map[string]struct {
					Auth bool `toml:"requires_openai_auth"`
				} `toml:"model_providers"`
			}
			for i := 0; i+1 < len(os.Args); i++ {
				if os.Args[i] == "-c" {
					_, _ = toml.Decode(os.Args[i+1], &settings)
					i++
				}
			}
			provider, ok := settings.Providers["mekugi_wrap"]
			if !ok || provider.Auth || settings.Model != "grok:grok-4.7" || !bytes.Contains(body, []byte("grok:grok-4.6")) || bytes.Contains(body, []byte("gpt-")) {
				os.Exit(98)
			}
		}
	}
	interrupts := make(chan os.Signal, 1)
	if os.Getenv("MEKUGI_TEST_EXIT") == "interrupt" {
		signal.Notify(interrupts, os.Interrupt)
	}
	var baseURL string
	for _, arg := range os.Args {
		start := strings.Index(arg, "http://127.0.0.1:")
		if start < 0 {
			continue
		}
		baseURL = strings.SplitN(arg[start:], "\"", 2)[0]
		break
	}
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
	os.Args = []string{os.Args[0], "grok"}
	if os.Getenv("MEKUGI_TEST_LAUNCH_MODE") == "standalone" {
		os.Args = []string{os.Args[0], "exec", "--yolo", "hello"}
	}
	os.Exit(run())
}

func TestStandaloneRunWithoutCodexLogin(t *testing.T) {
	directory := t.TempDir()
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("XAI_API_KEY", "test")
	t.Setenv("MEKUGI_TEST_ROUTER", "1")
	t.Setenv("MEKUGI_TEST_CODEX", "1")
	t.Setenv("MEKUGI_TEST_EXIT", "0")
	t.Setenv("MEKUGI_TEST_LAUNCH_MODE", "standalone")
	t.Setenv("MEKUGI_TEST_PINNED_CATALOG", "1")
	t.Setenv("MEKUGI_TEST_ADDRESS", filepath.Join(directory, "address"))
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	stub := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestWrappedCodexProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWrappedRouterProcess$")
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("codex stdout")) {
		t.Fatalf("standalone launch failed: %v\n%s", err, output)
	}
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
		grok       bool
	}{
		{"success", "0", 0, false}, {"failure", "23", 23, false}, {"signal", "signal", 143, false}, {"termination", "wait", 143, false},
		{"grok_success", "0", 0, true}, {"grok_failure", "23", 23, true}, {"grok_signal", "signal", 143, true}, {"grok_termination", "wait", 143, true},
	}
	if selected := os.Getenv("MEKUGI_TEST_WRAP_LIFECYCLE"); selected != "" {
		index, err := strconv.Atoi(selected)
		if err != nil || index < 0 || index >= len(tests) {
			t.Fatalf("invalid lifecycle selection %q", selected)
		}
		testWrapCodexLifecycle(t, tests[index].exit, tests[index].code, tests[index].grok)
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

func testWrapCodexLifecycle(t *testing.T, exit string, wantCode int, grok bool) {
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
	if grok {
		routerArgs = []string{"grok"}
		t.Setenv("MEKUGI_TEST_PINNED_CATALOG", "1")
	}
	code, err := wrapCodex(ctx, routerArgs, []string{"exec", "prompt with spaces"})
	if err != nil || code != wantCode {
		t.Fatalf("wrap = %d, %v; want %d", code, err, wantCode)
	}
	if grok {
		path, err := os.ReadFile(addressFile + ".catalog")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(string(path))); !os.IsNotExist(err) {
			t.Fatalf("private catalog survived child exit: %v", err)
		}
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
		if err := validateCodexArgs(args); err == nil {
			t.Errorf("accepted provider override: %q", args)
		}
	}
	for _, args := range [][]string{
		nil, {"exec", "a prompt with spaces"}, {"--profile", "work"},
		{"exec", "-c", `model="example"`}, {"--", "--oss"},
		{"--", `-cmodel_provider="other"`}, {"-c"},
	} {
		if err := validateCodexArgs(args); err != nil {
			t.Errorf("rejected ordinary arguments %q: %v", args, err)
		}
	}
}

func TestRunWrapRejectsUnauthenticatedStandalone(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
		t.Setenv(key, "")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "launched")
	t.Setenv("PATH", directory)
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte("#!/bin/sh\nprintf launched > "+strconv.Quote(marker)+"\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"exec", "--yolo", "hello"}} {
		if code := runWrap(nil, args); code != 1 {
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
			t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			marker := filepath.Join(directory, "launched")
			stub := "#!/bin/sh\ntouch " + strconv.Quote(marker) + "\n"
			if failure == "codex" {
				stub = "#!/nonexistent-mekugi-test-interpreter\n"
			} else {
				userConfig, err := os.UserConfigDir()
				if err != nil {
					t.Fatal(err)
				}
				plugins := filepath.Join(userConfig, "mekugi", "plugins")
				if err := os.MkdirAll(plugins, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(plugins, "broken.mjs"), []byte("this is not valid JavaScript"), 0o600); err != nil {
					t.Fatal(err)
				}
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

func TestWrapDebugPassesAXJournalToCodex(t *testing.T) {
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
	code, err := wrapCodex(t.Context(), []string{"--debug", "--mode", "passthrough"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("debug wrap = %d, %v", code, err)
	}
	path, err := os.ReadFile(marker)
	if err != nil || !filepath.IsAbs(string(path)) {
		t.Fatalf("AX journal was not inherited: %q, %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(string(path)), "ax.json")); err != nil {
		t.Fatalf("automatic report missing: %v", err)
	}
}

func TestCodexArgsJournalPlanOverridePreservesPassthrough(t *testing.T) {
	for _, journal := range []bool{false, true} {
		args := codexArgs("http://127.0.0.1:12345/v1", []string{"exec", "-c", "tools.update_plan.enabled=true", "--", "prompt"}, journal, false, true)
		end := slices.Index(args, "--")
		disabled := slices.Contains(args[:end], "tools.update_plan.enabled=false")
		if disabled != journal {
			t.Fatalf("plan override for journal=%v: %q", journal, args)
		}
	}
}

func TestWrapStartupInterrupt(t *testing.T) {
	directory := t.TempDir()
	temporary := t.TempDir()
	runtimeDirectory := t.TempDir()
	addressFile := filepath.Join(directory, "address")
	t.Setenv("TMPDIR", temporary)
	t.Setenv("XAI_API_KEY", "test")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("MEKUGI_TEST_ROUTER", "1")
	t.Setenv("MEKUGI_TEST_CODEX", "1")
	t.Setenv("MEKUGI_TEST_CATALOG_DELAY", "1")
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
	for {
		if _, err := os.Stat(addressFile + ".preparing"); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("wrapper exited before preparation: %v", err)
		case <-ctx.Done():
			t.Fatal("startup timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Signal only the wrapper: the bootstrap subprocess must be canceled by its owner.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || ctx.Err() != nil {
		t.Fatalf("startup interrupt did not stop promptly: %v, %v", err, ctx.Err())
	}
	if stdout.Len() != 0 ||
		!strings.Contains(logs.String(), "context canceled") ||
		strings.Contains(logs.String(), "private bootstrap diagnostic") ||
		strings.Contains(logs.String(), "configuration") ||
		strings.Contains(logs.String(), "mekugi dashboard:") {
		t.Fatalf("unexpected startup output: stdout=%q stderr=%q", stdout.String(), logs.String())
	}
	if _, err := os.Stat(addressFile); !os.IsNotExist(err) {
		t.Fatalf("interactive Codex launched after cancellation: %v", err)
	}
	for _, path := range []string{temporary, runtimeDirectory} {
		if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
			t.Fatalf("startup cancellation leaked resources: %v, %v", entries, err)
		}
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
	userZsh := t.TempDir()
	environment, err := frontendShellEnvironment([]string{"PATH=" + os.Getenv("PATH"), "ZDOTDIR=" + userZsh, "MEKUGI_TEST_TRACK_LOG=" + log}, frontend, helper, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(environment, "PATH="+frontend+":"+os.Getenv("PATH")) || !slices.Contains(environment, "ZDOTDIR="+userZsh) {
		t.Fatalf("unguarded environment = %q", environment)
	}
	guard, _ := vcsguard.Paths(frontend)
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("unguarded shell installed guard: %v", err)
	}
	const script = "printf local; printf read"
	command := exec.Command(bash, "-c", script)
	command.Env = environment
	if output, err := command.Output(); err != nil || string(output) != "localread" {
		t.Fatalf("unguarded command = %q, %v", output, err)
	}
	if tracked, err := os.ReadFile(log); err != nil || string(tracked) != script+"\n" {
		t.Fatalf("tracked script = %q, %v", tracked, err)
	}
}

func TestFrontendNestedGuardOptOut(t *testing.T) {
	for _, withZdotdir := range []bool{false, true} {
		t.Run(fmt.Sprintf("zdotdir=%t", withZdotdir), func(t *testing.T) {
			root := t.TempDir()
			outer, inner, real := filepath.Join(root, "outer", "bin"), filepath.Join(root, "inner", "bin"), filepath.Join(root, "user", vcsguard.Directory)
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
			userZsh := filepath.Join(root, "user-zsh")
			if withZdotdir {
				env = append(env, "ZDOTDIR="+userZsh)
			}
			env, err := frontendShellEnvironment(env, outer, helper, true)
			if err != nil {
				t.Fatal(err)
			}
			env, err = frontendShellEnvironment(env, inner, "", false)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range env {
				if value, ok := strings.CutPrefix(entry, "ZDOTDIR="); ok && (!withZdotdir || value != userZsh) {
					t.Fatalf("restored ZDOTDIR = %q", value)
				}
			}
			if withZdotdir && !slices.Contains(env, "ZDOTDIR="+userZsh) {
				t.Fatal("user ZDOTDIR lost")
			}
			for _, name := range []string{"git", filepath.Join(real, "git")} {
				cmd := exec.Command("/bin/bash", "-c", shellsyntax.Quote(name)+" push; printf ' %s' \"$USER_STARTUP\"")
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != "REAL_GIT preserved" {
					t.Fatalf("nested opt-out %q = %q, %v", name, out, err)
				}
			}
		})
	}
}
