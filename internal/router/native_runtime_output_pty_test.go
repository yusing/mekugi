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
	"unicode/utf8"

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
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	storeDirectory := t.TempDir()
	service, _, closeObservation := observationIsolationService(t, storeDirectory, binding)
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
	expected := first + "\n" + second + "\n"
	if background {
		// Large enough to exceed the native 8 KiB tail, with multibyte boundaries.
		var bulk strings.Builder
		for i := range 400 {
			fmt.Fprintf(&bulk, "完整輸出_%03d_🌲_café_abcdefghijklmnopqrstuvwxyz\n", i)
		}
		data := bulk.String()
		if err := os.WriteFile(filepath.Join(binding.Workspace, "output-bulk.txt"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		script = strings.Replace(script, "printf '%s\\n' "+last, "cat output-bulk.txt\nprintf '%s\\n' "+last, 1)
		if err := os.WriteFile(filepath.Join(binding.Workspace, "output-gates.sh"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		expected += data
	}
	expected += last + "\n"
	if background {
		// Native Claude appends its terminal receipt to the owned spool.
		expected += "\n[exited with code 0]\n"
	}
	var nativeSession, spool string
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
				if event.SessionID != "" {
					nativeSession = event.SessionID
				}
				if event.ID == "output-command-once" && event.Output != nil && event.Output.OutputFile != "" {
					spool = event.Output.OutputFile
				}
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
	terminal, stop := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
	stopTerminal := sync.OnceFunc(stop)
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
			runes := []rune(row)
			if activityClick {
				if len(runes) <= 60 {
					continue
				}
				prefix, search = string(runes[:60]), string(runes[60:])
			} else if len(runes) > 60 {
				search = string(runes[:60])
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
	await("settled terminal output", func(s string) bool {
		marker := last
		if !background {
			marker = "3 ┆ " + last
		}
		return strings.Contains(s, marker) && !strings.Contains(s, "● live")
	})
	if background {
		terminal.keys("g")
		await("complete terminal prefix", func(s string) bool {
			return strings.Contains(s, "1 ┆ "+first) && strings.Contains(s, "完整輸出_000_🌲_café") && !strings.Contains(s, "�")
		})
	}
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
	savedSession, savedSpool := nativeSession, spool
	mu.Unlock()
	if background {
		assertAggregate := func(s *ObservationService) {
			t.Helper()
			call := ObservationCall{Binding: ObservationBinding{Runtime: "claude", Workspace: binding.Workspace, Session: savedSession}, ID: "output-command-once", Tool: "Bash"}
			s.owner.mu.Lock()
			outputContext, err := s.owner.callContext(ctx, call)
			s.owner.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			history, found, err := s.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/output")
			if err != nil || !found || history.NativeOutput == nil {
				t.Fatalf("complete native output not retained: found=%v err=%v", found, err)
			}
			text, err := s.owner.store.readOutputChunks(outputContext, history.NativeOutput.Reference)
			if err != nil || text != expected || !utf8.ValidString(text) || strings.Contains(text, "�") {
				t.Fatalf("retained aggregate differs: got=%d want=%d UTF8=%v err=%v", len(text), len(expected), utf8.ValidString(text), err)
			}
		}
		assertAggregate(service)
		stopTerminal()
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		<-joined
		closeObservation()
		// Move only the spool identified by this native command, retaining recovery.
		if savedSession == "" || !filepath.IsAbs(savedSpool) {
			t.Fatalf("missing native session/spool identity: session=%q spool=%q", savedSession, savedSpool)
		}
		backup := filepath.Join(t.TempDir(), "native-output-spool")
		if err := os.Rename(savedSpool, backup); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Rename(backup, savedSpool); err != nil {
				t.Error(err)
			}
		})
		fresh, _, _ := observationIsolationService(t, storeDirectory, binding)
		freshPresentation, err := fresh.PrepareCompanion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		freshEndpoint := fresh.Endpoint()
		mu.Lock()
		requestsBeforeResume := workRequests
		mu.Unlock()
		freshClient, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: savedSession, Companion: &claude.ObservationEndpoint{Socket: freshEndpoint.Socket, Token: freshEndpoint.Token, Plugin: freshPresentation.Plugin, FrontendDirectory: freshPresentation.FrontendDirectory, JournalSchema: freshPresentation.JournalSchema}})
		if err != nil {
			t.Fatal(err)
		}
		defer freshClient.Close()
		terminal, stop = startNativeRuntimePTY(t, ctx, freshClient, binding.Workspace, fresh)
		stopTerminal = sync.OnceFunc(stop)
		await("restored native command", func(s string) bool {
			return strings.Contains(s, "Ran bash ./output-gates.sh") && strings.Contains(s, "┆ … +")
		})
		click("… +")
		await("restored settled dialog", func(s string) bool { return strings.Contains(s, "y copy · esc") && !strings.Contains(s, "● live") })
		terminal.keys("G")
		await("restored final output", func(s string) bool { return strings.Contains(s, last) })
		terminal.keys("g")
		await("restored complete prefix", func(s string) bool {
			return strings.Contains(s, "1 ┆ "+first) && strings.Contains(s, "完整輸出_000_🌲_café") && !strings.Contains(s, "�")
		})
		assertAggregate(fresh)
		mu.Lock()
		requestsAfterResume := workRequests
		mu.Unlock()
		if requestsAfterResume != requestsBeforeResume {
			t.Fatalf("resume replayed provider work: before=%d after=%d", requestsBeforeResume, requestsAfterResume)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tools) != 1 || !tools["output-command-once"] {
		t.Fatalf("native command identities: %v", tools)
	}
	var incremental, final, truncatedTail bool
	for _, event := range snapshots {
		if event.Output == nil || event.ID != "output-command-once" {
			t.Fatalf("output lost native identity: %+v", event)
		}
		incremental = incremental || strings.Contains(event.Text, first) && !event.Output.Done
		final = final || strings.Contains(event.Text, last) && event.Output.Done
		truncatedTail = truncatedTail || event.Output.Done && event.Output.Truncated && !strings.Contains(event.Text, first)
	}
	if !background {
		final = aggregate
	}
	if !incremental || !final {
		t.Fatalf("native snapshots did not stream and settle: incremental=%v final=%v", incremental, final)
	}
	if background && !truncatedTail {
		t.Fatal("background fixture did not exceed the native terminal tail")
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
		select {
		case <-uiDone:
		case <-time.After(3 * time.Second):
			t.Error("native terminal UI did not stop")
		}
		slave.Close()
		master.Close()
		<-readerDone
		terminal.screen.Close()
	}
	return terminal, stop
}
