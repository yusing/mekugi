package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		code, err := wrapCodex(t.Context(), []string{"--mentor-handoff=false"}, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
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
}

func TestWrapLiveDiffWaitsForStockActivityAndTerminal(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		t.Run(strconv.FormatBool(terminal), func(t *testing.T) {
			dir := t.TempDir()
			workspace := t.TempDir()
			marker := filepath.Join(dir, "launched")
			t.Setenv("MEKUGI_AUTO_WRAP_PROCESS", "1")
			t.Setenv("MEKUGI_AUTO_WRAP_WORKSPACE", workspace)
			t.Setenv("MEKUGI_AUTO_WRAP_MARKER", marker)
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			for name, script := range map[string]string{
				"codex": "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestAutoWrapProcess$ -- codex \"$@\"\n",
				"herdr": `#!/bin/sh
case "$2" in
split) printf '%s\n' '{"result":{"pane":{"pane_id":"new"}}}' ;;
close) printf '%s\n' "$3" > "$MEKUGI_AUTO_WRAP_MARKER.closed" ;;
run) printf '%s\n' "$@" > "$MEKUGI_AUTO_WRAP_MARKER" ;;
esac
`,
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
			if terminal {
				tty, startErr := pty.Start(cmd)
				if startErr != nil {
					t.Fatal(startErr)
				}
				defer tty.Close()
				read := make(chan []byte, 1)
				go func() { data, _ := io.ReadAll(tty); read <- data }()
				err = cmd.Wait()
				output = <-read
			} else {
				output, err = cmd.CombinedOutput()
			}
			if err != nil {
				t.Fatalf("wrapper: %v\n%s", err, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("wrapper launched live diff without a stock edit or command call")
			}
			if _, err := os.Stat(marker + ".closed"); !os.IsNotExist(err) {
				t.Fatal("wrapper closed a pane it never needed")
			}
			if bytes.Contains(output, []byte("Live diff started")) {
				t.Fatal("automatic launch wrote into Codex's terminal")
			}
		})
	}
}
