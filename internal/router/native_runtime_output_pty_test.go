//go:build unix

package router

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// This uses installed Claude with a local scripted provider, not inference.
// File gates are released only after actual terminal output becomes visible.
func TestNativeRuntimeOutputClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%v", background), func(t *testing.T) { nativeRuntimeOutputClaudePTY(t, background) })
	}
}

func nativeRuntimeOutputClaudePTY(t *testing.T, background bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "native-output-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	service, binding, _ := observationHTTPFixture(t)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const first = "PTY_OUTPUT_FIRST_CEDAR"
	const second = "PTY_OUTPUT_SECOND_MAPLE"
	const last = "PTY_OUTPUT_FINAL_BIRCH"
	script := "printf '%s\\n' " + first + "\nwhile [ ! -f output-second.gate ]; do sleep 0.05; done\nprintf '%s\\n' " + second + "\nwhile [ ! -f output-finish.gate ]; do sleep 0.05; done\nprintf '%s\\n' " + last + "\nprintf 'once\\n' >> output-executions.txt\n"
	if err := os.WriteFile(filepath.Join(binding.Workspace, "output-gates.sh"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	workRequests := 0
	tools := map[string]bool{}
	var snapshots []session.Event
	var aggregate bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		content := []any{map[string]any{"type": "text", "text": "OUTPUT_NATIVE_ACCEPTED"}}
		stop := "end_turn"
		if advertised, _ := packet["tools"].([]any); len(advertised) != 0 {
			workRequests++
			if workRequests > 8 {
				t.Error("native output exceeded request budget")
				w.WriteHeader(400)
				return
			}
			if workRequests == 1 {
				content = []any{map[string]any{"type": "tool_use", "id": "output-command-once", "name": "Bash", "input": map[string]any{"command": "bash ./output-gates.sh", "description": "Output PTY gates", "run_in_background": background}}}
				stop = "tool_use"
			}
		}
		nativeGuidanceProviderReply(w, packet, content, stop)
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Companion: &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer close(observed.events)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-client.Events():
				if !ok {
					return
				}
				mu.Lock()
				if event.Kind == "tool" && event.Role == "Bash" {
					tools[event.ID] = true
				}
				if event.Kind == "tool_result" && event.ID == "output-command-once" && strings.Contains(event.Text, last) {
					aggregate = true
				}
				if event.Kind == "command_output" {
					snapshots = append(snapshots, event)
				}
				mu.Unlock()
				select {
				case observed.events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	terminal, stopTerminal := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
	t.Cleanup(func() {
		// Release only this fixture's processes if an assertion fails at a gate.
		_ = os.WriteFile(filepath.Join(binding.Workspace, "output-second.gate"), nil, 0600)
		_ = os.WriteFile(filepath.Join(binding.Workspace, "output-finish.gate"), nil, 0600)
		cancel()
		stopTerminal()
		<-joined
	})
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(25*time.Second, func(frame string) bool {
			if strings.Contains(frame, "Permission · Bash") && strings.Contains(frame, "Allow once") {
				terminal.keys("1\r")
			}
			return match(frame)
		}) {
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	activityClick := false
	click := func(text string) {
		t.Helper()
		for y, row := range strings.Split(terminal.screen.String(), "\n") {
			search := row
			prefix := ""
			if activityClick {
				runes := []rune(row)
				if len(runes) <= 60 {
					continue
				}
				prefix, search = string(runes[:60]), string(runes[60:])
			}
			if at := strings.Index(search, text); at >= 0 {
				x := len([]rune(prefix+search[:at])) + 1
				terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y+1, x, y+1))
				return
			}
		}
		t.Fatalf("no rendered click target %q:\n%s", text, terminal.screen.String())
	}
	gate := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binding.Workspace, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	await("ready", func(s string) bool { return strings.Contains(s, "Ready") })
	terminal.paste("Execute the native output acceptance command.")
	await("incremental Main output", func(s string) bool { return strings.Contains(s, first) })
	click(first)
	await("live command dialog", func(s string) bool {
		return strings.Contains(s, "1 ┆ "+first) && strings.Contains(s, "y copy · esc") && strings.Contains(s, "● live")
	})
	gate("output-second.gate")
	await("second marker in open dialog", func(s string) bool { return strings.Contains(s, "2 ┆ "+second) && strings.Contains(s, "● live") })
	terminal.keys("\x1b")
	await("Main second marker", func(s string) bool { return strings.Contains(s, second) && !strings.Contains(s, "y copy · esc") })
	terminal.keys("\x02" + "3")
	activityClick = true
	await("Activity output", func(s string) bool { return strings.Contains(s, second) })
	click(second)
	await("Activity command dialog", func(s string) bool {
		return strings.Contains(s, "2 ┆ "+second) && strings.Contains(s, "y copy · esc")
	})
	gate("output-finish.gate")
	await("settled terminal output", func(s string) bool { return strings.Contains(s, "3 ┆ "+last) && !strings.Contains(s, "● live") })
	terminal.keys("\x1b")
	await("Activity task completion", func(s string) bool {
		return strings.Contains(s, "Output PTY gates · completed") && !strings.Contains(s, "y copy · esc")
	})
	click("Output PTY gates · completed")
	await("Activity Events dialog", func(s string) bool {
		return strings.Contains(s, "Output PTY gates · completed") && strings.Contains(s, "y copy · esc")
	})
	terminal.keys("\x1b")
	await("closed Events dialog", func(s string) bool { return !strings.Contains(s, "y copy · esc") })
	terminal.keys("\x02" + "1")
	activityClick = false
	await("Main task completion", func(s string) bool { return strings.Contains(s, "Output PTY gates · completed") })
	click("Output PTY gates · completed")
	await("Main Events dialog", func(s string) bool {
		return strings.Contains(s, "Output PTY gates · completed") && strings.Contains(s, "y copy · esc")
	})
	mu.Lock()
	defer mu.Unlock()
	if len(tools) != 1 || !tools["output-command-once"] {
		t.Fatalf("native command identities: %v", tools)
	}
	var incremental, final bool
	for _, event := range snapshots {
		if event.Output == nil || event.ID != "output-command-once" {
			t.Fatalf("output lost native identity: %+v", event)
		}
		incremental = incremental || strings.Contains(event.Text, first) && !event.Output.Done
		final = final || strings.Contains(event.Text, last) && event.Output.Done
	}
	if !background {
		final = aggregate
	}
	if !incremental || !final {
		t.Fatalf("native snapshots did not stream and settle: incremental=%v final=%v", incremental, final)
	}
	count, err := os.ReadFile(filepath.Join(binding.Workspace, "output-executions.txt"))
	if err != nil || string(count) != "once\n" {
		t.Fatalf("native command did not run exactly once: %q %v", count, err)
	}
}

// Run the shared UI in-process; subprocess PTY fixtures own their lifecycle separately.
func startNativeRuntimePTY(t *testing.T, parent context.Context, client session.Client, cwd string, service *ObservationService) (*nativeClaudePTY, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 36}); err != nil {
		t.Fatal(err)
	}
	terminal := &nativeClaudePTY{t: t, ctx: ctx, master: master, frames: make(chan []byte, 128), screen: vt.NewEmulator(120, 36)}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var pending []byte
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				at := bytes.Index(pending, []byte("\x1b[?2026l"))
				if at < 0 {
					break
				}
				at += len("\x1b[?2026l")
				frame := bytes.Clone(pending[:at])
				frame = bytes.ReplaceAll(frame, []byte("\x1b]10;?\x1b\\"), nil)
				frame = bytes.ReplaceAll(frame, []byte("\x1b]11;?\x1b\\"), nil)
				select {
				case terminal.frames <- frame:
				case <-ctx.Done():
					return
				}
				pending = pending[at:]
			}
			if err != nil {
				return
			}
		}
	}()
	uiDone := make(chan error, 1)
	go func() {
		uiDone <- RunNativeSession(ctx, client, "Claude Code", cwd, slave, slave, service)
	}()
	stop := func() {
		cancel()
		slave.Close()
		master.Close()
		<-readerDone
		select {
		case <-uiDone:
		case <-time.After(3 * time.Second):
			t.Error("native terminal UI did not stop")
		}
		terminal.screen.Close()
	}
	return terminal, stop
}
