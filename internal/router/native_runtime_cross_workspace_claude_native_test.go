//go:build unix

package router

import (
	"context"
	json "encoding/json/v2"
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

// Ordinary native transcripts, SDK metadata and native Bash effects are the
// authority here. The provider is local and deterministic, with no inference.
func TestNativeRuntimeCrossWorkspaceClaudeNative(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires installed native Claude and built bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	configDirectory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDirectory)
	// Local providers exercise native permissions without a real Auto classifier.
	if err := os.WriteFile(filepath.Join(configDirectory, "settings.json"), []byte(`{"permissions":{"defaultMode":"default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "cross-workspace-fixture")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	a := t.TempDir()
	b := filepath.Join(t.TempDir(), "workspace B with spaces")
	if err := os.Mkdir(b, 0700); err != nil {
		t.Fatal(err)
	}
	for i, cwd := range []string{a, b} {
		commands := filepath.Join(cwd, ".claude", "commands")
		if err := os.MkdirAll(commands, 0700); err != nil {
			t.Fatal(err)
		}
		name := []string{"native-workspace-a", "native-workspace-b"}[i]
		if err := os.WriteFile(filepath.Join(commands, name+".md"), []byte("Describe this workspace without tools.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const markerA = "NATIVE_WORKSPACE_CEDAR_1287"
	const markerB = "NATIVE_WORKSPACE_MAPLE_9352"
	packets := make(chan map[string]any, 24)
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
		content := []any{map[string]any{"type": "text", "text": "CROSS_WORKSPACE_ACCEPTED"}}
		stop := "end_turn"
		if tools, _ := packet["tools"].([]any); len(tools) > 0 {
			select {
			case packets <- packet:
			default:
				t.Error("native request budget exceeded")
			}
			messages := nativeGuidanceRequestText(packet["messages"])
			// Native engine attachments can follow the user's prompt. The real
			// tool-use identity, not message position, prevents another execution.
			if strings.Contains(messages, "SEED_NATIVE ") && !strings.Contains(messages, "seed-workspace-effect") {
				content = []any{map[string]any{"type": "tool_use", "id": "seed-workspace-effect", "name": "Bash", "input": map[string]any{"command": "pwd; printf 'once\\n' >> native-once.txt", "description": "Seed ordinary native workspace effect"}}}
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
	start := func(cwd, resume string, endpoint *claude.ObservationEndpoint) *claude.Client {
		t.Helper()
		c, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: cwd, Resume: resume, Executable: executable, Model: "haiku", Companion: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	drain := func() []map[string]any {
		var out []map[string]any
		for {
			select {
			case p := <-packets:
				out = append(out, p)
			default:
				return out
			}
		}
	}
	seed := func(cwd, marker, title string) string {
		t.Helper()
		c := start(cwd, "", nil)
		id := ""
		for {
			select {
			case <-ctx.Done():
				t.Fatal("ordinary native seed timeout")
			case e, ok := <-c.Events():
				if !ok || e.Kind == "error" || e.Failed {
					t.Fatalf("ordinary seed failed: %#v", e)
				}
				switch e.Kind {
				case "ready":
					if err := c.Send(ctx, "SEED_NATIVE "+marker); err != nil {
						t.Fatal(err)
					}
				case "session":
					id = e.SessionID
				case "prompt":
					if err := c.Respond(ctx, session.Decision{ID: e.Prompt.ID, Allow: true}); err != nil {
						t.Fatal(err)
					}
				case "done":
					if err := c.RenameSession(ctx, session.SessionTitle{ID: "seed-title", SessionID: id, Title: title}); err != nil {
						t.Fatal(err)
					}
				case "title":
					if id == "" {
						t.Fatal("seed identity missing")
					}
					c.Close()
					drain()
					return id
				}
			}
		}
	}
	idA := seed(a, markerA, "Native workspace A")
	idB := seed(b, markerB, "Native workspace B 修復")
	binding := ObservationBinding{Runtime: "claude", Workspace: a}
	store := t.TempDir()
	service, _, closeObservation := observationIsolationService(t, store, binding)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client := start(a, "", &claude.ObservationEndpoint{Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema})
	u := newRuntimeUI(ctx, client, "Claude Code", a)
	u.attachRuntimeObservation(service)
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	wait := func(kind string, failed bool) session.Event {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native cross-workspace timeout waiting for %s", kind)
			case e, ok := <-client.Events():
				if !ok || e.Kind == "error" {
					t.Fatalf("native cross-workspace disconnected: %#v", e)
				}
				if err := u.runtimeEvent(e); err != nil {
					t.Fatal(err)
				}
				if e.Kind == kind {
					if e.Failed != failed {
						t.Fatalf("%s failed=%v: %s", kind, e.Failed, e.Text)
					}
					return e
				}
			}
		}
	}
	switchTo := func(id, cwd, title, marker, absent string) {
		t.Helper()
		runtimeKeys(t, u, "/resume "+id+"\r")
		wait("session_ready", false)
		selected, departing := "native-workspace-a", "native-workspace-b"
		if cwd == b {
			selected, departing = departing, selected
		}
		assertNativeWorkspaceCommands(t, u.runtime.commandInfo, selected, departing)
		frame := runtimeFrame(t, u, 120, 30)
		if u.thread != id || u.session.cwd != cwd || u.shell.diff.workspace != cwd || u.title != title || !strings.Contains(frame, marker) || strings.Contains(frame, absent) {
			t.Fatalf("native history/title/workspace mismatch: thread=%s cwd=%s title=%s\n%s", u.thread, u.session.cwd, u.title, frame)
		}
	}
	assertPrompt := func(marker, absent, cwd string) {
		t.Helper()
		requests := drain()
		if len(requests) != 1 {
			t.Fatalf("unexpected model request count: %d", len(requests))
		}
		text := nativeGuidanceRequestText(requests[0])
		if !strings.Contains(text, marker) || strings.Contains(text, absent) || !strings.Contains(nativeGuidanceRequestText(requests[0]["messages"]), "Primary working directory: "+cwd) {
			t.Fatal("native model context lost history/current environment or crossed sessions")
		}
		assertNativeFrontendContracts(t, service.registry, requests[0]["messages"])
		skill, err := os.ReadFile(filepath.Join(service.plugin, "skills", "mekugi", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		_, workflow, ok := strings.Cut(string(skill), "\n---\n")
		if !ok || !strings.Contains(text, strings.TrimSpace(workflow)) {
			t.Fatal("resumed native request omitted complete current workflow")
		}
	}
	prompt := func(marker, absent, cwd string) {
		t.Helper()
		runtimeKeys(t, u, "CHECK_NATIVE_WORKSPACE\r")
		wait("done", false)
		assertPrompt(marker, absent, cwd)
	}
	wait("ready", false)
	switchTo(idA, a, "Native workspace A", markerA, markerB)
	switchTo(idB, b, "Native workspace B 修復", markerB, markerA)
	prompt(markerB, markerA, b)
	switchTo(idA, a, "Native workspace A", markerA, markerB)
	runtimeKeys(t, u, "/resume 00000000-0000-4000-8000-000000000001\rRetry cross-workspace input\r")
	wait("session_ready", true)
	if u.thread != idA || u.session.cwd != a || u.draft != "Retry cross-workspace input" || !u.runtime.ready {
		t.Fatal("failed preflight discarded source session/workspace/draft")
	}
	u.draft = ""
	prompt(markerA, markerB, a)
	client.Close()
	u.shell.diff.close()
	u.shell.diffScreen.Close()
	closeObservation()
	fresh, _, _ := observationIsolationService(t, store, binding)
	freshPresentation, err := fresh.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	freshEndpoint := fresh.Endpoint()
	client = start(a, idB, &claude.ObservationEndpoint{Socket: freshEndpoint.Socket, Token: freshEndpoint.Token, Plugin: freshPresentation.Plugin, FrontendDirectory: freshPresentation.FrontendDirectory, JournalSchema: freshPresentation.JournalSchema})
	service = fresh
	nativeCrossWorkspacePTY(t, ctx, client, fresh, a, idB, b, markerA, markerB, func() { assertPrompt(markerB, markerA, b) })
	for _, cwd := range []string{a, b} {
		data, err := os.ReadFile(filepath.Join(cwd, "native-once.txt"))
		if err != nil || string(data) != "once\n" {
			t.Fatalf("native effects replayed or crossed workspace: %q %v", data, err)
		}
	}
}

// Exercise the actual terminal picker, including the workspace filter. No
// synthetic UI events replace native receipts or replayed SDK transcripts.
func nativeCrossWorkspacePTY(t *testing.T, parent context.Context, client *claude.Client, service *ObservationService, launchCwd, idB, resumedCwd, markerA, markerB string, assertPrompt func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	observed := &nativeClaudePTYClient{Client: client, events: make(chan session.Event)}
	receipts := make(chan session.Event, 8)
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
				if event.Kind == "session" || event.Kind == "done" || event.Kind == "ready" {
					select {
					case receipts <- event:
					case <-ctx.Done():
						return
					}
				}
				select {
				case observed.events <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	terminal, stopTerminal := startNativeRuntimePTY(t, ctx, observed, launchCwd, service)
	defer func() {
		cancel()
		stopTerminal()
		<-joined
	}()
	waitReceipt := func(kind string) session.Event {
		t.Helper()
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("native terminal timeout waiting for %s", kind)
			case event := <-receipts:
				if event.Kind == kind {
					if event.Failed {
						t.Fatalf("native %s failed: %+v", kind, event)
					}
					return event
				}
			}
		}
	}
	await := func(label string, match func(string) bool) {
		t.Helper()
		if !terminal.observe(20*time.Second, match) {
			t.Fatalf("terminal missing %s:\n%s", label, terminal.screen.String())
		}
	}
	await("fresh native B history", func(f string) bool {
		return strings.Contains(f, markerB) && strings.Contains(f, "Native workspace B 修復") && !strings.Contains(f, markerA) && strings.Contains(f, "Ready")
	})
	restored := waitReceipt("session")
	if restored.SessionID != idB || restored.Cwd != resumedCwd || restored.Title == nil || restored.Title.Title != "Native workspace B 修復" {
		t.Fatalf("fresh A-launch B-resume lost native identity/workspace/title: %+v", restored)
	}
	assertNativeWorkspaceCommands(t, waitReceipt("ready").CommandInfo, "native-workspace-b", "native-workspace-a")
	terminal.paste("CHECK_NATIVE_WORKSPACE")
	waitReceipt("done")
	assertPrompt()
	terminal.keys("/resume\r")
	await("shared session picker", func(f string) bool {
		return strings.Contains(f, "Resume a previous session") && strings.Contains(f, "Native workspace B")
	})
	terminal.keys("\t")
	await("all-workspace native list", func(f string) bool {
		return strings.Contains(f, "Native workspace A") && strings.Contains(f, "Native workspace B")
	})
	terminal.keys("Native workspace A")
	await("native picker search", func(f string) bool {
		return strings.Contains(f, "Search: Native workspace A") && strings.Contains(f, "Native workspace A")
	})
	terminal.keys("\r")
	await("native A resume", func(f string) bool {
		return !strings.Contains(f, "Resume a previous session") && strings.Contains(f, markerA) && !strings.Contains(f, markerB) && strings.Contains(f, "Ready")
	})
	terminal.keys("/resume\r")
	await("A workspace picker", func(f string) bool {
		return strings.Contains(f, "Resume a previous session") && strings.Contains(f, "Native workspace A")
	})
	terminal.keys("\t")
	await("B in native list", func(f string) bool { return strings.Contains(f, "Native workspace B") })
	terminal.keys("Native workspace B")
	await("B picker search", func(f string) bool { return strings.Contains(f, "Search: Native workspace B") })
	terminal.keys("\r")
	await("native B return", func(f string) bool {
		return !strings.Contains(f, "Resume a previous session") && strings.Contains(f, markerB) && !strings.Contains(f, markerA) && strings.Contains(f, "Ready")
	})
}

func assertNativeWorkspaceCommands(t *testing.T, commands []session.Command, selected, departing string) {
	t.Helper()
	var found bool
	for _, command := range commands {
		if command.Name == departing {
			t.Fatalf("native command catalog retained departing workspace command %q", departing)
		}
		found = found || command.Name == selected
	}
	if !found {
		t.Fatalf("native command catalog omitted selected workspace command %q: %+v", selected, commands)
	}
}
