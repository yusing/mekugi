package launch

import (
	"bufio"
	"bytes"
	"context"
	"errors"

	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestHeadlessWrapRedirectedEntryPoint(t *testing.T) {
	dir, workspace := installHeadlessFixture(t)
	t.Setenv("MEKUGI_AUTO_WRAP_TRUST", "true")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoWrapProcess$", "--", "router")
	prompt := "  /literal prompt\n!not a shell command\n"
	t.Setenv("MEKUGI_AUTO_WRAP_PROMPT", prompt)
	cmd.Dir, cmd.Stdin = workspace, strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		methods, _ := os.ReadFile(filepath.Join(dir, "rpc"))
		t.Fatalf("headless: %v\n%s\n%s\n%s", err, &stderr, output, methods)
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var last string
	for scanner.Scan() {
		var event appserver.Message
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("non-JSON stdout: %q: %v", scanner.Text(), err)
		}
		last = event.Method
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last != "mekugi/headless/completed" {
		t.Fatalf("missing terminal event: %s", output)
	}
	methods, err := os.ReadFile(filepath.Join(dir, "rpc"))
	if err != nil || string(methods) != "initialize\ninitialized\nconfig/read\ninitialize\ninitialized\nenvironment/info\nthread/start\nmodel/list\nconfig/read\nturn/start\n" {
		t.Fatalf("RPC sequence: %s (%v)", methods, err)
	}
}

func TestHeadlessRejectsUnsupportedAuthorizationAndResume(t *testing.T) {
	for _, args := range [][]string{{"headless"}, {"headless", "--yolo", "resume", "thread"}, {"headless", "--yolo", "prompt"}} {
		code, err := wrapCodex(t.Context(), nil, args)
		if code != 2 || err == nil {
			t.Fatalf("%v: code=%d err=%v", args, code, err)
		}
	}
}

func TestChildRuntimeOutputSeparation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "capture.jsonl")
	reads := filepath.Join(t.TempDir(), "reads.jsonl")
	setup := &threadSetup{options: launchoptions.Config{CaptureOutput: root}, environment: []string{"MEKUGI_AX_OUTPUT=" + reads}}
	paths := map[string]bool{root: true, reads: true}
	for range 3 { // Siblings and a descendant all borrow the frozen family setup.
		options, environment, err := setup.childOutputs()
		if err != nil {
			t.Fatal(err)
		}
		path := options.CaptureOutput
		readPath := strings.TrimPrefix(environment[0], "MEKUGI_AX_OUTPUT=")
		if paths[path] || paths[readPath] || filepath.Dir(path) != filepath.Dir(readPath) || filepath.Dir(filepath.Dir(path)) != root+".runtimes" {
			t.Fatalf("overlapping runtime outputs: %q, %q", path, readPath)
		}
		paths[path], paths[readPath] = true, true
		info, err := os.Stat(filepath.Dir(path))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("private output directory: %v, %v", info, err)
		}
	}
	if setup.options.CaptureOutput != root || setup.environment[0] != "MEKUGI_AX_OUTPUT="+reads {
		t.Fatal("mutated family output selection")
	}
}

type headlessFailureWriter struct {
	err    error
	cancel context.CancelFunc
}

func (w headlessFailureWriter) Write(p []byte) (int, error) {
	if w.cancel != nil {
		w.cancel()
		return len(p), nil
	}
	return 0, w.err
}

func TestHeadlessAdmittedOutputFailureAndCancellation(t *testing.T) {
	for _, scenario := range []string{"output", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			dir, _ := installHeadlessFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			failure := errors.New("output failed")
			writer := headlessFailureWriter{err: failure}
			if scenario == "cancel" {
				writer.cancel = cancel
			}
			code, err := runHeadless(ctx, []string{"--shell-guard=false"}, []string{"--yolo"}, strings.NewReader("literal"), writer)
			if code != 1 || err == nil {
				t.Fatalf("code=%d err=%v", code, err)
			}
			if scenario == "output" && !errors.Is(err, failure) || scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost failure: %v", err)
			}
			entries, readErr := os.ReadDir(os.Getenv("MEKUGI_RUNTIME_DIR"))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("runtime resources survived: %v, %v", entries, readErr)
			}
			if _, err := os.Stat(filepath.Join(dir, "rpc.joined")); err != nil {
				t.Fatalf("host cleanup not joined: %v", err)
			}
			methods, readErr := os.ReadFile(filepath.Join(dir, "rpc"))
			if readErr != nil || scenario == "output" && bytes.Contains(methods, []byte("thread/start")) {
				t.Fatalf("interpreted undelivered response: %s (%v)", methods, readErr)
			}
		})
	}
}

func TestHeadlessAdmittedFailures(t *testing.T) {
	for _, scenario := range []string{"startup", "approval", "question", "disconnect", "failed", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			dir, workspace := installHeadlessFixture(t)
			t.Setenv("MEKUGI_AUTO_WRAP_HEADLESS_SCENARIO", scenario)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoWrapProcess$", "--", "router")
			cmd.Dir, cmd.Stdin = workspace, strings.NewReader("literal prompt")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err == nil || bytes.Contains(output, []byte("mekugi/headless/completed")) {
				t.Fatalf("accepted %s: err=%v output=%s", scenario, err, output)
			}
			want := scenario
			switch scenario {
			case "startup":
				want = "fixture: invalid model configuration"
			case "disconnect":
				want = "fixture: host transport ended"
			case "approval", "question":
				want = "continue interactively"
			}
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("lost %s diagnostic: %s", scenario, &stderr)
			}
			if bytes.Contains(output, []byte("fixture:")) {
				t.Fatalf("host stderr leaked into JSONL: %s", output)
			}
			for _, row := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
				if len(row) == 0 {
					continue
				}
				var message appserver.Message
				if err := json.Unmarshal(row, &message); err != nil {
					t.Fatalf("non-JSON stdout: %s", row)
				}
			}
			if scenario != "startup" && scenario != "disconnect" {
				if _, err := os.Stat(filepath.Join(dir, "rpc.joined")); err != nil {
					t.Fatalf("host shutdown not joined: %v", err)
				}
			}
		})
	}
}

func installHeadlessFixture(t *testing.T) (string, string) {
	t.Helper()
	dir, workspace := t.TempDir(), t.TempDir()
	t.Setenv("MEKUGI_AUTO_WRAP_PROCESS", "1")
	t.Setenv("MEKUGI_AUTO_WRAP_HEADLESS", "1")
	t.Setenv("MEKUGI_AUTO_WRAP_GUARD", "off")
	t.Setenv("MEKUGI_AUTO_WRAP_FAILURE", "0")
	t.Setenv("MEKUGI_AUTO_WRAP_RPC_MARKER", filepath.Join(dir, "rpc"))
	t.Setenv("MEKUGI_AUTO_WRAP_WORKSPACE", workspace)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestAutoWrapProcess$ -- codex \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir, workspace
}

func TestHeadlessLiteralCommandText(t *testing.T) {
	for _, prompt := range []string{"/compact", "/compact\n", "/quit", "!pwd"} {
		t.Run(strings.ReplaceAll(prompt, "\n", "newline"), func(t *testing.T) {
			dir, workspace := installHeadlessFixture(t)
			t.Chdir(workspace)
			t.Setenv("MEKUGI_AUTO_WRAP_PROMPT", prompt)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var output bytes.Buffer
			code, err := runHeadless(ctx, nil, []string{"--yolo"}, strings.NewReader(prompt), &output)
			if code != 0 || err != nil || !strings.Contains(output.String(), "mekugi/headless/completed") {
				t.Fatalf("literal prompt %q: code=%d err=%v\n%s", prompt, code, err, &output)
			}
			methods, err := os.ReadFile(filepath.Join(dir, "rpc"))
			if err != nil || bytes.Contains(methods, []byte("thread/compact/start")) || bytes.Count(methods, []byte("turn/start\n")) != 1 {
				t.Fatalf("literal prompt dispatched as command: %s (%v)", methods, err)
			}
		})
	}
}

func TestHeadlessCorrelatedSubmissionFailure(t *testing.T) {
	for _, scenario := range []string{"rpc-rejected", "rpc-stale"} {
		t.Run(scenario, func(t *testing.T) {
			dir, workspace := installHeadlessFixture(t)
			t.Chdir(workspace)
			t.Setenv("MEKUGI_AUTO_WRAP_HEADLESS_SCENARIO", scenario)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var output bytes.Buffer
			code, err := runHeadless(ctx, nil, []string{"--yolo"}, strings.NewReader("ordinary prompt"), &output)
			if scenario == "rpc-rejected" {
				if code != 1 || err == nil || !strings.Contains(err.Error(), "turn/start: fixture: start rejected") || errors.Is(err, context.DeadlineExceeded) || strings.Contains(output.String(), "mekugi/headless/completed") {
					t.Fatalf("lost correlated rejection: code=%d err=%v\n%s", code, err, &output)
				}
				if !strings.Contains(output.String(), "fixture: start rejected") {
					t.Fatal("rejection was interpreted before its raw envelope was emitted")
				}
			} else if code != 0 || err != nil || !strings.Contains(output.String(), "mekugi/headless/completed") {
				t.Fatalf("uncorrelated rejection changed submission: code=%d err=%v\n%s", code, err, &output)
			}
			methods, readErr := os.ReadFile(filepath.Join(dir, "rpc"))
			if readErr != nil || bytes.Count(methods, []byte("turn/start\n")) != 1 {
				t.Fatalf("submission was repeated: %s (%v)", methods, readErr)
			}
			if _, err := os.Stat(filepath.Join(dir, "rpc.joined")); err != nil {
				t.Fatalf("host shutdown was not joined: %v", err)
			}
		})
	}
}

func TestHeadlessPreservesNoGuardPolicy(t *testing.T) {
	for _, selection := range []string{"default", "configured", "explicit-on", "explicit-off"} {
		t.Run(selection, func(t *testing.T) {
			dir, workspace := installHeadlessFixture(t)
			t.Chdir(workspace)
			var routerArgs []string
			switch selection {
			case "configured":
				configuration := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mekugi")
				if err := os.MkdirAll(configuration, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configuration, "config.toml"), []byte("[features]\nshell_guard = true\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "explicit-on":
				routerArgs = []string{"--shell-guard=true"}
			case "explicit-off":
				routerArgs = []string{"--shell-guard=false"}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var output bytes.Buffer
			code, err := runHeadless(ctx, routerArgs, []string{"--yolo"}, strings.NewReader("ordinary prompt"), &output)
			if code != 0 || err != nil {
				t.Fatalf("headless acquired interactive guards: code=%d err=%v", code, err)
			}
			methods, readErr := os.ReadFile(filepath.Join(dir, "rpc"))
			if readErr != nil || bytes.Contains(methods, []byte("hooks/list")) {
				t.Fatalf("headless performed guard admission: %s (%v)", methods, readErr)
			}
		})
	}
}
