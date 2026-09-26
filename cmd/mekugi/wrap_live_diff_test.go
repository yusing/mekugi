package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestAutoWrapProcess(t *testing.T) {
	if os.Getenv("MEKUGI_AUTO_WRAP_PROCESS") != "1" {
		return
	}
	if os.Args[3] == "router" {
		var args []string
		if os.Getenv("MEKUGI_AUTO_WRAP_TERMINAL") == "1" {
			args = []string{"--yolo"}
		}
		code, err := wrapCodex(t.Context(), []string{"--mentor-handoff=false"}, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	if len(os.Args) > 4 && os.Args[4] == "app-server" {
		marker, err := os.OpenFile(os.Getenv("MEKUGI_AUTO_WRAP_RPC_MARKER"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer marker.Close()
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(marker, request.Method)
			switch request.Method {
			case "initialize":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{}}`+"\n", request.ID)
			case "thread/start":
				fmt.Fprintf(os.Stdout, `{"id":%s,"result":{"thread":{"id":"auto-wrap","cwd":%q},"model":"test-model"}}`+"\n", request.ID, os.Getenv("MEKUGI_AUTO_WRAP_WORKSPACE"))
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Keep the test runner's PASS line out of the RPC stream.
	}
	workspace := os.Getenv("MEKUGI_AUTO_WRAP_WORKSPACE")
	body := `{"model":"gpt-test","input":[{"role":"user","content":"test"}],"tools":[{"type":"function","name":"exec_command","description":"run a command"},{"type":"custom","name":"apply_patch","description":"apply a patch"}],"tool_choice":"auto"}`
	request, err := http.NewRequest("POST", os.Getenv("MEKUGI_BASE_URL")+"/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"request_kind": "turn", "workspaces": map[string]any{workspace: nil}})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-codex-turn-metadata", string(metadata))
	request.Header.Set("thread-id", "auto-wrap")
	request.Header.Set("session-id", "auto-wrap")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// Deliberately omit authentication: preparation succeeds, forwarding fails
	// locally, and this fixture never contacts an upstream provider.
	if !bytes.Contains(data, []byte("Authorization")) {
		t.Fatalf("request did not reach forwarding: %s", data)
	}
	// Keep the fake child alive long enough for the integrated terminal's first
	// frame to be rendered before the wrapper joins its lifetime.
	time.Sleep(300 * time.Millisecond)
}

func TestWrapIntegratedUIAndRedirectedBehavior(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal bool
		optOut   bool
	}{{"terminal", true, false}, {"terminal-env-zero", true, true}, {"redirected", false, false}} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			workspace := t.TempDir()
			herdrMarker := filepath.Join(dir, "herdr-invoked")
			rpcMarker := filepath.Join(dir, "app-server-rpc")
			t.Setenv("MEKUGI_AUTO_WRAP_PROCESS", "1")
			t.Setenv("MEKUGI_AUTO_WRAP_TERMINAL", "0")
			if test.terminal {
				t.Setenv("MEKUGI_AUTO_WRAP_TERMINAL", "1")
			}
			t.Setenv("MEKUGI_AUTO_WRAP_RPC_MARKER", rpcMarker)
			t.Setenv("MEKUGI_APP_SERVER_UI", "")
			if test.optOut {
				t.Setenv("MEKUGI_APP_SERVER_UI", "0")
			} else if err := os.Unsetenv("MEKUGI_APP_SERVER_UI"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MEKUGI_AUTO_WRAP_WORKSPACE", workspace)
			t.Setenv("MEKUGI_TEST_HERDR_MARKER", herdrMarker)
			t.Setenv("HERDR_ENV", "")
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			// Keep installed runtime tools such as Node.js available while placing
			// a failing Herdr sentinel first, so the wrapper can prove it never
			// invokes the pane manager.
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			for name, script := range map[string]string{
				"codex": "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestAutoWrapProcess$ -- codex \"$@\"\n",
				"herdr": "#!/bin/sh\nprintf 'invoked\\n' > \"$MEKUGI_TEST_HERDR_MARKER\"\nexit 97\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoWrapProcess$", "--", "router")
			var output []byte
			var err error
			if test.terminal {
				tty, startErr := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
				if startErr != nil {
					t.Fatal(startErr)
				}
				defer tty.Close()
				read := make(chan []byte, 1)
				go func() {
					var data bytes.Buffer
					buf := make([]byte, 8192)
					quit := false
					for {
						n, readErr := tty.Read(buf)
						data.Write(buf[:n])
						if !quit && bytes.Contains(data.Bytes(), []byte("Ready")) {
							_, _ = tty.Write([]byte("/quit\r"))
							quit = true
						}
						if readErr != nil {
							break
						}
					}
					read <- data.Bytes()
				}()
				err = cmd.Wait()
				output = <-read
			} else {
				output, err = cmd.CombinedOutput()
			}
			if err != nil {
				t.Fatalf("wrapper: %v\n%s", err, output)
			}
			if _, err := os.Stat(herdrMarker); !os.IsNotExist(err) {
				t.Fatalf("wrapper invoked Herdr: %v", err)
			}
			if test.terminal {
				if !bytes.Contains(output, []byte("\x1b[?1049h")) || !bytes.Contains(output, []byte("1 Main")) {
					t.Fatalf("terminal wrapper did not render native Main: %q", output)
				}
				calls, readErr := os.ReadFile(rpcMarker)
				if readErr != nil || !bytes.Contains(calls, []byte("initialize\ninitialized\nthread/start\n")) {
					t.Fatalf("native app-server handshake: %q, %v", calls, readErr)
				}
			} else if bytes.Contains(output, []byte("\x1b[?1049h")) || bytes.Contains(output, []byte("1 Main")) {
				t.Fatalf("redirected wrapper unexpectedly rendered the integrated UI: %q", output)
			} else if _, statErr := os.Stat(rpcMarker); !os.IsNotExist(statErr) {
				t.Fatalf("redirected wrapper unexpectedly started app-server: %v", statErr)
			}
		})
	}
}

func TestInteractiveCodexArgs(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{nil, true}, {[]string{"resume", "--last"}, true}, {[]string{"--model", "exec", "hello"}, true},
		{[]string{"exec", "hello"}, false}, {[]string{"--model", "gpt-6", "exec", "hello"}, false},
		{[]string{"--help"}, false}, {[]string{"--version"}, false}, {[]string{"-c", "help", "prompt"}, true},
	} {
		if got := interactiveCodexArgs(test.args); got != test.want {
			t.Errorf("%v: %v, want %v", test.args, got, test.want)
		}
	}
}
