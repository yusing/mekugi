//go:build unix

package toolplugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// Dedicated helpers isolate cwd, PATH, environment and process ownership.
func TestResolverCleanupRetiresInheritedPipeDescendants(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"direct", "frontend"} {
		for _, kind := range []string{"gopls", "lsp"} {
			outcomes := []string{"success", "failure", "cancellation", "deadline"}
			if kind == "lsp" {
				outcomes = append(outcomes, "ignore_shutdown", "exit", "queued_success", "queued_no_response")
			}
			for _, outcome := range outcomes {
				t.Run(owner+"/"+kind+"/"+outcome, func(t *testing.T) {
					t.Parallel()
					directory, err := filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					name, source, resolver := "input.go", "package p\nvar Target = 1\n", "gopls"
					if kind == "lsp" {
						name, source, resolver = "input.ts", "const Target = 1;\n", "tsc"
					}
					if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0600); err != nil {
						t.Fatal(err)
					}
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
					script := "#!/bin/sh\nexec " + quote(executable) + " -test.run='^TestResolverCleanupProcess$' -- resolver\n"
					if err := os.WriteFile(filepath.Join(directory, resolver), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					pidPath := filepath.Join(directory, "descendant.pid")
					t.Cleanup(func() {
						if data, err := os.ReadFile(pidPath); err == nil {
							if pid, err := strconv.Atoi(string(data)); err == nil {
								_ = syscall.Kill(pid, syscall.SIGKILL)
							}
						}
					})
					ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, executable, "-test.run=^TestResolverCleanupProcess$", "--", "execute")
					cmd.Dir = directory
					cmd.Env = append(os.Environ(), "PATH="+directory, "MEKUGI_CLEANUP_FIXTURE="+directory,
						"FIXTURE_OWNER="+owner, "FIXTURE_KIND="+kind, "FIXTURE_OUTCOME="+outcome, "FIXTURE_SOURCE="+filepath.Join(directory, name))
					ConfigureProcessGroup(cmd)
					cmd.WaitDelay = time.Second
					output, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("native resolver cleanup: %v\n%s", err, output)
					}
					if string(output) != "PASS\n" {
						t.Fatalf("unexpected helper output: %q", output)
					}
				})
			}
		}
	}
}

// Virtual time validates the 30-second session deadline without a process sleep
// or production timer seam. Subprocess cases validate real interruption cleanup.
func TestNativeResolverSessionDeadline(t *testing.T) {
	if nativeResolverTimeout != 30*time.Second || nativeResolverDrain != time.Second {
		t.Fatalf("resolver timing = %s / %s, want 30s / 1s", nativeResolverTimeout, nativeResolverDrain)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), nativeResolverTimeout)
		defer cancel()
		server := nativeLSP{input: io.Discard, events: make(chan nativeRPCEvent)}
		started := time.Now()
		_, err := server.request(ctx, "textDocument/references", nil)
		var failure *symbolFailure
		if !errors.As(err, &failure) || failure.class != "resolver_timeout" || time.Since(started) != 30*time.Second {
			t.Fatalf("session deadline: %v after %s", err, time.Since(started))
		}
	})
}

func TestResolverCleanupProcess(t *testing.T) {
	directory := os.Getenv("MEKUGI_CLEANUP_FIXTURE")
	if directory == "" {
		return
	}
	role := os.Args[len(os.Args)-1]
	if role == "descendant" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if role == "resolver" {
		runCleanupResolver(t, directory)
		os.Exit(0)
	}
	if role != "execute" {
		t.Fatal("unknown helper role")
	}
	// Adopt killed descendants for reaping, without frontend cleanup masking a
	// broken owned process group in the direct API cases.
	if err := enableFrontendSubreaper(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if os.Getenv("FIXTURE_OWNER") == "frontend" {
		ctx = WithHostProcessGroup(ctx)
		var err error
		ctx, err = EnableFrontendOrphanCleanup(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	outcome, kind := os.Getenv("FIXTURE_OUTCOME"), os.Getenv("FIXTURE_KIND")
	if outcome == "deadline" {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, 500*time.Millisecond)
		defer deadlineCancel()
	}
	if outcome == "cancellation" {
		go func() {
			for {
				if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
					cancel()
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Millisecond):
				}
			}
		}()
	}
	name, row, line := "input.go", "2", "var Target = 1"
	if kind == "lsp" {
		name, row, line = "input.ts", "1", "const Target = 1;"
	}
	started := time.Now()
	result, err := ExecuteBuiltin(ctx, "msymbol", []string{"refs", name, row, "Target"})
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("cleanup exceeded bound: %s", elapsed)
	}
	switch outcome {
	case "cancellation", "deadline":
		want := context.Canceled
		if outcome == "deadline" {
			want = context.DeadlineExceeded
		}
		if !errors.Is(err, want) {
			t.Fatalf("interruption = %+v, %v; want %v", result, err, want)
		}
	case "success", "ignore_shutdown", "queued_success":
		want := strconv.Quote(name) + ":\n" + row + " " + line + "\n"
		if err != nil || result.ExitCode != 0 || result.Stdout != want || result.Stderr != "" {
			t.Fatalf("semantic result: %+v, %v", result, err)
		}
	default:
		if err != nil || result.ExitCode != 1 || result.Stdout != "" || result.Stderr == "" {
			t.Fatalf("resolver failure: %+v, %v", result, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "descendant.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	// Reap only, never kill: missed retirement must fail this assertion.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resolver descendant %d survived cleanup", pid)
}

func runCleanupResolver(t *testing.T, directory string) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outcome := os.Getenv("FIXTURE_OUTCOME")
	child := exec.Command(executable, "-test.run=^TestResolverCleanupProcess$", "--", "descendant")
	if !strings.HasPrefix(outcome, "queued_") {
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "descendant.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FIXTURE_OWNER") == "direct" && group != os.Getpid() {
		t.Fatal("direct resolver does not own its group")
	}
	if os.Getenv("FIXTURE_OWNER") == "frontend" {
		parentGroup, err := syscall.Getpgid(os.Getppid())
		if err != nil || group != parentGroup {
			t.Fatalf("frontend resolver escaped caller group: %d / %d, %v", group, parentGroup, err)
		}
	}
	ready := func() {
		if err := os.WriteFile(filepath.Join(directory, "ready"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("FIXTURE_KIND") == "gopls" {
		switch outcome {
		case "success":
			fmt.Printf("%s:2:5-11\n", os.Getenv("FIXTURE_SOURCE"))
			return
		case "failure":
			fmt.Fprintln(os.Stderr, "query failed")
			os.Exit(1)
		default:
			ready()
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	frame := func(v any) []byte {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Appendf(nil, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
	respond := func(id jsontext.Value, result any) {
		if _, err := os.Stdout.Write(frame(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})); err != nil {
			os.Exit(0)
		}
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length := -1
		for {
			header, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if header == "\r\n" {
				break
			}
			if value, ok := strings.CutPrefix(strings.TrimSpace(header), "Content-Length:"); ok {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length < 0 || length > 1<<20 {
			t.Fatal("invalid request length")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return
		}
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		switch message.Method {
		case "initialize":
			if outcome == "exit" {
				return
			}
			encoding := "utf-16"
			if outcome == "failure" {
				encoding = "utf-8"
			}
			respond(message.ID, map[string]any{"capabilities": map[string]string{"positionEncoding": encoding}})
		case "textDocument/references":
			if outcome == "cancellation" || outcome == "deadline" {
				ready()
				continue
			}
			result := []symbolFixtureLocation{nativeSymbolFixtureLocation(os.Getenv("FIXTURE_SOURCE"), 0, 6, 12)}
			if strings.HasPrefix(outcome, "queued_") {
				var buffered bytes.Buffer
				for i := range 10000 {
					buffered.Write(frame(map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{"type": 3, "message": strconv.Itoa(i)}}))
				}
				if outcome == "queued_success" {
					buffered.Write(frame(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}))
				}
				if _, err := os.Stdout.Write(buffered.Bytes()); err != nil {
					t.Fatal(err)
				}
				return
			}
			respond(message.ID, result)
		case "shutdown":
			if outcome != "ignore_shutdown" {
				respond(message.ID, nil)
			}
		case "exit":
			return
		}
	}
}
