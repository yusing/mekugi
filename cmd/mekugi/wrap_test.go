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
)

func TestCodexArgsPreservesArguments(t *testing.T) {
	forwarded := []string{"exec", "-c", "model=\"example\"", "--", "a prompt with spaces"}
	args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true)
	index := slices.Index(forwarded, "--")
	if !slices.Equal(args[:index], forwarded[:index]) || !slices.Equal(args[index+8:], forwarded[index:]) {
		t.Fatalf("forwarded arguments changed: %q", args)
	}
	var config struct {
		IncludeCollaborationModeInstructions *bool  `toml:"include_collaboration_mode_instructions"`
		ModelProvider                        string `toml:"model_provider"`
		Providers                            map[string]struct {
			Name       string `toml:"name"`
			BaseURL    string `toml:"base_url"`
			WireAPI    string `toml:"wire_api"`
			WebSockets bool   `toml:"supports_websockets"`
			Auth       bool   `toml:"requires_openai_auth"`
		} `toml:"model_providers"`
	}
	var settings []string
	for i := index; i < index+8; i += 2 {
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
	provider := config.Providers[config.ModelProvider]
	if provider.Name == "" || provider.BaseURL != "http://127.0.0.1:12345/v1" || provider.WireAPI != "responses" || !provider.Auth || !provider.WebSockets {
		t.Fatalf("provider = %+v", provider)
	}
	withoutDelimiter := []string{"exec", "-c", `model="example"`, "prompt"}
	if got := codexArgs("http://127.0.0.1:12345/v1", withoutDelimiter, true); !slices.Equal(got[:len(withoutDelimiter)], withoutDelimiter) {
		t.Fatalf("ordinary -c or prompt moved: %q", got)
	}
}

func TestCodexArgsEnforcesCollaborationModeInstructions(t *testing.T) {
	for _, forwarded := range [][]string{
		{"-c", "include_collaboration_mode_instructions=true"},
		{"-c", "include_collaboration_mode_instructions=true", "exec", "--config=include_collaboration_mode_instructions=true", "prompt"},
		{"resume", "session", "-cinclude_collaboration_mode_instructions=true", "--", "prompt"},
	} {
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, true)
		end := slices.Index(args, "--")
		if end < 0 {
			end = len(args)
		}
		if !slices.Equal(args[end-2:end], []string{"-c", "include_collaboration_mode_instructions=false"}) {
			t.Fatalf("enforced override is not last before delimiter: %q", args)
		}
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
	if os.Getenv("MEKUGI_TEST_PINNED_CATALOG") == "1" {
		var catalogPath string
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "model_catalog_json=") {
				catalogPath, _ = strconv.Unquote(strings.TrimPrefix(arg, "model_catalog_json="))
			}
		}
		body, err := os.ReadFile(catalogPath)
		if err != nil || !bytes.Contains(body, []byte("grok:grok-4.6")) || !bytes.Contains(body, []byte("freeform")) {
			os.Exit(98)
		}
		if err := os.WriteFile(os.Getenv("MEKUGI_TEST_ADDRESS")+".catalog", []byte(catalogPath), 0o600); err != nil {
			os.Exit(99)
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
	os.Args = []string{os.Args[0], "--grok", "--model-protocol", "native", "--mentor-handoff=false", "codex"}
	os.Exit(run())
}

func TestWrapTerminalInterruptAndTermination(t *testing.T) {
	directory := t.TempDir()
	logDirectory := t.TempDir()
	t.Setenv("TMPDIR", logDirectory)
	runtimeDirectory := t.TempDir()
	addressFile := filepath.Join(directory, "address")
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
	announcement := "mekugi dashboard: " + strings.TrimSuffix(baseURL, "/v1") + "/\n"
	if !strings.HasPrefix(logs.String(), announcement) || strings.Count(logs.String(), "mekugi dashboard: ") != 1 {
		t.Fatalf("dashboard announcement must precede Codex output exactly once: %q", logs.String())
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
	for _, test := range []struct {
		name, exit string
		code       int
		grok       bool
	}{
		{"success", "0", 0, false}, {"failure", "23", 23, false}, {"signal", "signal", 143, false}, {"termination", "wait", 143, false},
		{"grok_success", "0", 0, true}, {"grok_failure", "23", 23, true}, {"grok_signal", "signal", 143, true}, {"grok_termination", "wait", 143, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("TMPDIR", t.TempDir())
			runtimeDirectory := t.TempDir()
			addressFile := filepath.Join(directory, "address")
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
			t.Setenv("MEKUGI_TEST_CODEX", "1")
			t.Setenv("MEKUGI_TEST_EXIT", test.exit)
			t.Setenv("MEKUGI_TEST_ADDRESS", addressFile)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			stub := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestWrappedCodexProcess$ -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.exit == "wait" {
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
			if test.grok {
				routerArgs = []string{"--grok"}
				t.Setenv("MEKUGI_TEST_PINNED_CATALOG", "1")
			}
			code, err := wrapCodex(ctx, routerArgs, []string{"exec", "prompt with spaces"})
			if err != nil || code != test.code {
				t.Fatalf("wrap = %d, %v; want %d", code, err, test.code)
			}
			if test.grok {
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
		})
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

func TestRunWrapUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"other"}} {
		if code := runWrap(nil, args); code != 2 {
			t.Errorf("runWrap(%q) = %d", args, code)
		}
	}
}

func TestWrapCodexStartupFailures(t *testing.T) {
	for _, failure := range []string{"router", "codex"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("TMPDIR", t.TempDir())
			runtimeDirectory := t.TempDir()
			configDirectory := t.TempDir()
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
		args := codexArgs("http://127.0.0.1:12345/v1", []string{"exec", "-c", "tools.update_plan.enabled=true", "--", "prompt"}, journal)
		end := slices.Index(args, "--")
		disabled := slices.Contains(args[:end], "tools.update_plan.enabled=false")
		if disabled != journal {
			t.Fatalf("plan override for journal=%v: %q", journal, args)
		}
	}
}
