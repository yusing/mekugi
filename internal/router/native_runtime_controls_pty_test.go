//go:build unix

package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Native Claude owns transcripts and controls; only model responses are local.
// Copies of a completed native transcript exercise SDK metadata pagination
// without turning fifty model requests into a picker fixture.
func TestNativeRuntimeSessionControlsClaudePTY(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("ANTHROPIC_API_KEY", "session-controls-pty-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	packets := make(chan string, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(401)
			return
		}
		var packet map[string]any
		if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &packet); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			messages, _ := json.Marshal(packet["messages"])
			select {
			case packets <- string(messages):
			default:
				t.Error("native prompt budget exceeded")
			}
		}
		nativeGuidanceProviderReply(w, packet, []any{map[string]any{"type": "text", "text": "NATIVE_PTY_CONTROLS_ACCEPTED"}}, "end_turn")
	}))
	defer provider.Close()
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL)
	binding := ObservationBinding{Runtime: "claude", Workspace: t.TempDir()}
	service, _, closeService := observationIsolationService(t, t.TempDir(), binding)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
	launch := func(client *claude.Client, service *ObservationService) (*nativeClaudePTY, func(), <-chan session.Event) {
		t.Helper()
		forwardCtx, stopForward := context.WithCancel(ctx)
		observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
		receipts := make(chan session.Event, 128)
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			defer close(observed.events)
			for {
				select {
				case <-forwardCtx.Done():
					return
				case event, ok := <-client.Events():
					if !ok {
						return
					}
					switch event.Kind {
					case "ready", "session", "done", "title", "sessions", "session_ready", "error":
						select {
						case receipts <- event:
						case <-forwardCtx.Done():
							return
						}
					}
					select {
					case observed.events <- event:
					case <-forwardCtx.Done():
						return
					}
				}
			}
		}()
		terminal, stopTerminal := startNativeRuntimePTY(t, ctx, observed, binding.Workspace, service)
		return terminal, func() { stopForward(); stopTerminal(); <-joined }, receipts
	}
	terminal, stop, receipts := launch(client, service)
	defer func() { stop() }()
	wait := func(kind string) session.Event {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native controls timed out waiting for %s:\n%s", kind, terminal.screen.String())
			case event := <-receipts:
				if event.Kind == "error" {
					t.Fatalf("native controls: %s", event.Text)
				}
				if event.Kind == kind {
					return event
				}
			}
		}
	}
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(15*time.Second, match) {
			t.Fatalf("missing %s:\n%s", label, terminal.screen.String())
		}
	}
	selectedTitle := func(f, title string) bool {
		for line := range strings.SplitSeq(f, "\n") {
			if strings.Contains(line, "› ") && strings.Contains(line, title) {
				return true
			}
		}
		return false
	}
	ready := func(f string) bool {
		return strings.Contains(f, "Ready") && !strings.Contains(f, "Resume a previous session")
	}
	prompt := func(text string) string {
		t.Helper()
		terminal.paste(text)
		if e := wait("done"); e.Failed {
			t.Fatalf("native prompt failed: %+v", e)
		}
		await("settled prompt", func(f string) bool { return ready(f) && strings.Contains(f, text) })
		select {
		case p := <-packets:
			return p
		case <-ctx.Done():
			t.Fatal("native request missing")
			return ""
		}
	}
	wait("ready")
	await("initial native UI", ready)
	const markerA = "PTY_SESSION_CEDAR_8151"
	const markerB = "PTY_SESSION_MAPLE_3392"
	const title = "Native PTY title 修復"
	prompt(markerA)
	terminal.paste("!printf 'once\\n' >> controls-once.txt; printf CONTROL_EFFECT_ACCEPTED")
	await("native seed effect", func(f string) bool {
		return ready(f) && strings.Contains(f, "context retained") && strings.Contains(f, "CONTROL_EFFECT_ACCEPTED")
	})
	assertOnce := func() {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(binding.Workspace, "controls-once.txt"))
		if err != nil || string(data) != "once\n" {
			t.Fatalf("native controls replayed effect: %q, %v", data, err)
		}
	}
	assertOnce()
	idA := service.owner.session
	if idA == "" {
		t.Fatal("native session identity missing")
	}
	terminal.keys("/title " + title + "\r")
	if e := wait("title"); e.Failed {
		t.Fatalf("native title failed: %+v", e)
	}
	await("native renamed title", func(f string) bool { return ready(f) && strings.Contains(f, title) })
	// Locate only this isolated native transcript, then retain its real SDK format.
	var transcript string
	err = filepath.WalkDir(filepath.Join(config, "projects"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == idA+".jsonl" {
			transcript = path
		}
		return nil
	})
	if err != nil || transcript == "" {
		t.Fatalf("native transcript missing: %v", err)
	}
	data, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+i)
		copied := strings.ReplaceAll(string(data), idA, id)
		copied = strings.ReplaceAll(copied, title, fmt.Sprintf("Paging native %02d", i))
		path := filepath.Join(filepath.Dir(transcript), id+".jsonl")
		if err := os.WriteFile(path, []byte(copied), 0600); err != nil {
			t.Fatal(err)
		}
		// Deterministic SDK lastModified order puts the search target on page three.
		modified := time.Now().Add(time.Duration(100-i) * time.Minute)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	terminal.keys("/resume\r")
	first := wait("sessions")
	if first.Failed || first.Sessions == nil || len(first.Sessions.Sessions) != 25 || first.Sessions.Cursor != "25" {
		t.Fatalf("native first page: %+v", first)
	}
	await("native paged list", func(f string) bool {
		return strings.Contains(f, "Resume a previous session") && strings.Contains(f, "Paging native 00")
	})
	terminal.keys("Paging native 50")
	await("search across native pages", func(f string) bool {
		return strings.Contains(f, "Search: Paging native 50") && selectedTitle(f, "Paging native 50") && !strings.Contains(f, "Paging native 00")
	})
	// Escape first clears search; the next Escape closes without resuming.
	terminal.keys("\x1b")
	await("cleared picker search", func(f string) bool {
		return strings.Contains(f, "Type to search") && strings.Contains(f, "Paging native")
	})
	terminal.keys("\x1b")
	await("cancelled picker", func(f string) bool { return ready(f) && strings.Contains(f, markerA) && strings.Contains(f, title) })
	terminal.keys("/resume\r")
	await("reopened picker", func(f string) bool { return strings.Contains(f, "Resume a previous session") })
	terminal.keys("\x03")
	await("Ctrl-C picker cancellation", func(f string) bool { return ready(f) && strings.Contains(f, markerA) })
	terminal.keys("/clear\r")
	if e := wait("session_ready"); e.Failed {
		t.Fatalf("native clear: %+v", e)
	}
	await("empty clear", func(f string) bool { return ready(f) && !strings.Contains(f, markerA) && !strings.Contains(f, title) })
	if p := prompt(markerB); strings.Contains(p, markerA) || !strings.Contains(p, markerB) {
		t.Fatal("clear crossed native context")
	}
	// Enter resumes the saved, natively titled A, rather than a test UI injection.
	terminal.keys("/resume\r")
	await("picker after clear", func(f string) bool { return strings.Contains(f, "Resume a previous session") })
	terminal.keys(title)
	await("saved A match", func(f string) bool { return strings.Contains(f, "Search: "+title) && selectedTitle(f, title) })
	terminal.keys("\r")
	if e := wait("session_ready"); e.Failed {
		t.Fatalf("native resume: %+v", e)
	}
	await("A history restoration", func(f string) bool {
		return ready(f) && strings.Contains(f, title) && strings.Contains(f, markerA) && !strings.Contains(f, markerB)
	})
	terminal.keys("/resume 00000000-0000-4000-8000-000000000001\rRetry retained draft\r")
	if e := wait("session_ready"); !e.Failed {
		t.Fatal("missing native session unexpectedly resumed")
	}
	await("failed resume retains source and draft", func(f string) bool {
		return ready(f) && strings.Contains(f, "Could not change session") && strings.Contains(f, "Retry retained draft") && strings.Contains(f, markerA) && strings.Contains(f, title)
	})
	terminal.keys("\x03")
	await("retained draft cleared", func(f string) bool { return ready(f) && !strings.Contains(f, "Retry retained draft") })
	select {
	case p := <-packets:
		t.Fatalf("controls replayed native work: %s", p)
	default:
	}
	assertOnce()
	stop()
	stop = func() {}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	closeService()
	freshBinding := binding
	freshBinding.Session = idA
	fresh, _, _ := observationIsolationService(t, service.owner.store.directory, freshBinding)
	freshPresentation, err := fresh.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	freshEndpoint := fresh.Endpoint()
	client, err = claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: "haiku", Resume: idA, Companion: &claude.ObservationEndpoint{Socket: freshEndpoint.Socket, Token: freshEndpoint.Token, Plugin: freshPresentation.Plugin, FrontendDirectory: freshPresentation.FrontendDirectory, JournalSchema: freshPresentation.JournalSchema}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal, stop, receipts = launch(client, fresh)
	wait("ready")
	await("fresh native title and history before input", func(f string) bool {
		return ready(f) && strings.Contains(f, title) && strings.Contains(f, markerA) && !strings.Contains(f, markerB)
	})
	assertOnce()
	select {
	case p := <-packets:
		t.Fatalf("fresh restoration replayed native work: %s", p)
	default:
	}
	if p := prompt("PTY_FRESH_RESUME_CHECK"); !strings.Contains(p, markerA) || strings.Contains(p, markerB) {
		t.Fatal("fresh native context crossed cleared session")
	}
	assertOnce()
}
