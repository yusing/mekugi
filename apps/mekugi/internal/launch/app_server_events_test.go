package launch

import (
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/inference"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func appServerTestNotify(t *testing.T, u *appServerUI, method string, params any) {
	t.Helper()
	wire, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.message(appserver.Message{Method: method, Params: jsontext.Value(wire)}); err != nil {
		t.Fatal(err)
	}
}

func newAppServerSessionTestUI(t *testing.T, workspace string) *appServerUI {
	u, _ := newAppServerTestUI()
	u.ctx = t.Context()
	u.segments = testReports()
	t.Cleanup(func() { awaitCommandSegments(t, u) })

	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.startSession("main", workspace)
	return u
}

func TestAppServerAgentTimerStartsAtFirstTurn(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	u.attached.session.Caller("early")
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/worker", Kind: "status", Text: "Assigned", Observed: time.Now()}}})
	for _, agent := range u.attached.session.Roster() {
		if !agent.Started.IsZero() {
			t.Fatalf("thread registration started timer: %+v", agent)
		}
		if _, timer := u.testActivity(u.shell.Agents).testCurrent(agent, time.Now().Add(time.Hour)); timer != "" {
			t.Fatalf("unstarted thread shows timer %q", timer)
		}
	}
	for _, thread := range []string{"main", "child", "early"} {
		before := time.Now()
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first"}})
		agent := u.attached.session.Agent(u.attached.session.KnownCaller(thread))
		start := agent.Started
		if start.Before(before) || start.After(time.Now()) {
			t.Fatalf("timer does not use turn start: %+v", agent)
		}
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "first", "status": "completed"}})
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "second"}})
		if !agent.Started.Equal(start) {
			t.Fatal("subsequent turn reset session timer")
		}
	}
}

func TestAppServerRestoredAgentTimerUsesTurns(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	info := binding.ThreadInfo{ID: "main", CreatedAt: 100, UpdatedAt: 900}
	restoreMainHistory(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, u.retained, u.retainedSession, u.activity, u.segments, &u.restoredSegments, info.Turns, nil, nil)
	agent := u.attached.session.Agent("/root")
	if !agent.Started.IsZero() || !agent.LastResponse.IsZero() {
		t.Fatal("empty restored thread started timer")
	}
	info.Turns = []binding.HistoryTurn{{ID: "a", StartedAt: 200, CompletedAt: 250}, {ID: "b", StartedAt: 300, CompletedAt: 350}}
	restoreMainHistory(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, u.retained, u.retainedSession, u.activity, u.segments, &u.restoredSegments, info.Turns, nil, nil)
	if !agent.Started.Equal(historyTime(200)) || !agent.LastResponse.Equal(historyTime(350)) {
		t.Fatalf("restored timer used metadata instead of turns: %+v", agent)
	}

}

func TestAppServerSessionProjectsChildThreads(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	setTestAppServerIntegration(u, &observationFixture{usage: newTestUsage(t)})
	u.usage.Observation("child", "child", "gpt-6-luna", "").Observe(inference.Usage{InputTokens: 1200, UncachedInputTokens: 1200, OutputTokens: 30})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "explore",
		"agentNickname": strings.TrimPrefix("/root/worker", "/root/")}})
	u.attached.session.Agent("/root/worker").Settings.Model = "gpt-6-luna"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": coordinationTestCall(t, "spawn", "spawn_agent", "child", u.attached.session.Caller("child"), "Inspect the pane.")})
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "c1"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "cmd", "type": "commandExecution",
		"command": "go test ./...", "exitCode": 2, "commandActions": []map[string]any{{"type": "unknown", "command": "go test ./..."}}}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "read", "type": "commandExecution",
		"command": "cat a.go", "exitCode": 0, "commandActions": []map[string]any{{"type": "read", "command": "cat", "path": "a.go"}}}})
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "turnId": "c1", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1200, "outputTokens": 30}}})
	agent := u.attached.session.Agent("/root/worker")
	if agent == nil || agent.Role != "explore" || !agent.Responding || agent.InputTokens != 1200 || agent.OutputTokens != 30 {
		t.Fatalf("roster did not follow the child thread: %+v", u.attached.session.Roster())
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "The pane waits on its first frame."}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "c1", "status": "completed"}})
	agent = u.attached.session.Agent("/root/worker")
	if agent.Responding || !agent.Final {
		t.Fatal("finished child still responding")
	}
	_, done := u.testActivity(u.shell.Agents).testCurrent(*agent, agent.LastResponse)
	_, later := u.testActivity(u.shell.Agents).testCurrent(*agent, agent.LastResponse.Add(time.Hour))
	if elapsed, _, _ := strings.Cut(done, " · "); !strings.HasPrefix(later, elapsed+" · ") {
		t.Fatalf("elapsed time kept counting after the child finished: %q, then %q", done, later)
	}
	activity := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(90, 60).Lines, "\n"))
	for _, want := range []string{"worker · explore", "▶ main → worker started · gpt-6-luna", "Inspect the pane.", "├ Ran  go test ./... · exit 2", "└ Read a.go", "✓ answer", "The pane waits on its first frame."} {
		if !strings.Contains(activity, want) {
			t.Fatalf("Activity lacks %q:\n%s", want, activity)
		}
	}
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	main := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 60).Lines, "\n"))
	for _, want := range []string{"▶ main → worker started", "├─✓ finished", "The pane waits on its first frame."} {
		if !strings.Contains(main, want) {
			t.Fatalf("Main lacks %q:\n%s", want, main)
		}
	}
	if strings.Contains(main, "↩ re:") {
		t.Fatalf("an answer threaded under its assignment quoted it again:\n%s", main)
	}
	if strings.Contains(main, "go test") {
		t.Fatal("a child's command reached Main")
	}
}

// Native patch events must not race the router's pre-execution source snapshot.
func TestAppServerSessionDocksSharedPreviewsByCaller(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.review.Projection.Scope = previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	// The router captures the source before host execution. App-server may
	// deliver its notifications only after the workspace has already changed.
	preview := projectStockPatchPreview(t.Context(), workspace, previewmodel.Preview{
		ID: "patch", Workspace: workspace, Thread: "main", Caller: "/root", Tool: applyPatchToolName,
		Status: previewmodel.PreviewEdit, Input: "*** Begin Patch\n*** Update File: a.go\n@@\n-package a\n+package b\n*** End Patch\n",
	})
	if err := os.WriteFile(path, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	change := map[string]any{"path": path, "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-package a\n+package b\n"}
	appServerTestNotify(t, u, "item/fileChange/patchUpdated", map[string]any{"threadId": "main", "turnId": "t", "itemId": "patch", "changes": []any{change}})
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "patch", "type": "fileChange", "changes": []any{change}}})
	if len(terminalTest(u).Live.Order) != 0 {
		t.Fatal("app-server reconstructed a duplicate projection")
	}
	applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "preview", Preview: &preview})
	revealNativeDock(terminalTest(u))
	card := terminalTest(u).Live.Views["patch"].Current
	if len(card.Files) != 1 || !strings.Contains(card.Files[0].Diff, "+package b") || strings.Contains(card.Status, "cannot be projected") {
		t.Fatalf("shared preview lost: %+v", card)
	}
	preview.Complete = true
	applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "preview", Preview: &preview})
	revealNativeDock(terminalTest(u))
	if !terminalTest(u).Live.Views["patch"].Complete {
		t.Fatal("shared completion lost")
	}
	for _, status := range []string{"failed", "declined"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "patch", "type": "fileChange", "status": status, "changes": []any{change}}})
		title := ansi.Strip(terminalTest(u).Live.Views["patch"].Title(workspace, terminalTest(u).Theme, 120))
		if strings.Contains(title, "✓") || !strings.Contains(title, "· preview") {
			t.Fatalf("%s patch presented as applied: %s", status, title)
		}
	}
	preview.ID, preview.Thread, preview.Caller, preview.Tool = "exec", "child", "/root/worker", nativeExecCommandToolName
	applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "preview", Preview: &preview})
	revealNativeDock(terminalTest(u))
	if terminalTest(u).Live.Views["exec"] != nil {
		t.Fatal("completion-only shell preview flashed a dock")
	}
	preview.Complete = false
	applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "preview", Preview: &preview})
	revealNativeDock(terminalTest(u))
	if terminalTest(u).Live.Views["exec"] == nil {
		t.Fatal("child preview missing from shared dock")
	}
	applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "turn", TurnRevision: 1, Status: "completed"})
	if terminalTest(u).DiffOpen {
		t.Fatal("completed turn opened saved diff")
	}
}
