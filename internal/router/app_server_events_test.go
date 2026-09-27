package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/ui/diffview"
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
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", workspace)
	return u
}

func TestAppServerSessionProjectsChildThreads(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "explore",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/worker"}}}}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "spawn", "type": "collabAgentToolCall",
		"tool": "spawnAgent", "senderThreadId": "main", "receiverThreadIds": []string{"child"}, "prompt": "Inspect the pane.", "model": "gpt-6-luna"}})
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "c1"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "cmd", "type": "commandExecution",
		"command": "go test ./...", "exitCode": 2, "commandActions": []map[string]any{{"type": "unknown", "command": "go test ./..."}}}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "read", "type": "commandExecution",
		"command": "cat a.go", "exitCode": 0, "commandActions": []map[string]any{{"type": "read", "command": "cat", "path": "a.go"}}}})
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "turnId": "c1", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1200, "outputTokens": 30}}})
	agent := u.session.agent("/root/worker")
	if agent == nil || agent.Role != "explore" || !agent.Responding || agent.InputTokens != 1200 || agent.OutputTokens != 30 {
		t.Fatalf("roster did not follow the child thread: %+v", u.session.agents)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "c1", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "The pane waits on its first frame."}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "c1", "status": "completed"}})
	agent = u.session.agent("/root/worker")
	if agent.Responding || !agent.Final {
		t.Fatal("finished child still responding")
	}
	_, done := u.agents.current(*agent, agent.LastResponse)
	_, later := u.agents.current(*agent, agent.LastResponse.Add(time.Hour))
	if elapsed, _, _ := strings.Cut(done, " · "); !strings.HasPrefix(later, elapsed+" · ") {
		t.Fatalf("elapsed time kept counting after the child finished: %q, then %q", done, later)
	}
	activity := ansi.Strip(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"))
	for _, want := range []string{"worker · explore", "▶ started · gpt-6-luna", "Inspect the pane.", "├ Ran  go test ./... · exit 2", "└ Read a.go", "✓ answer", "The pane waits on its first frame."} {
		if !strings.Contains(activity, want) {
			t.Fatalf("Activity lacks %q:\n%s", want, activity)
		}
	}
	u.view.conversation = true
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
	for _, want := range []string{"▶ worker started", "├─✓ finished", "The pane waits on its first frame.", "╰─↩ Open reply in Activity"} {
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
	u.shell.diff.scope = liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	// The router captures the source before host execution. App-server may
	// deliver its notifications only after the workspace has already changed.
	preview := projectStockPatchPreview(t.Context(), workspace, diffview.Preview{
		ID: "patch", Workspace: workspace, Thread: "main", Caller: "/root", Tool: applyPatchToolName,
		Status: diffview.PreviewEdit, Input: "*** Begin Patch\n*** Update File: a.go\n@@\n-package a\n+package b\n*** End Patch\n",
	})
	if err := os.WriteFile(path, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	change := map[string]any{"path": path, "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-package a\n+package b\n"}
	appServerTestNotify(t, u, "item/fileChange/patchUpdated", map[string]any{"threadId": "main", "turnId": "t", "itemId": "patch", "changes": []any{change}})
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "patch", "type": "fileChange", "changes": []any{change}}})
	if len(u.shell.mainDock.Order) != 0 {
		t.Fatal("app-server reconstructed a duplicate projection")
	}
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview})
	card := u.shell.mainDock.Views["patch"].Current
	if len(card.Files) != 1 || !strings.Contains(card.Files[0].Diff, "+package b") || strings.Contains(card.Status, "cannot be projected") {
		t.Fatalf("shared preview lost: %+v", card)
	}
	preview.Complete = true
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview})
	if !u.shell.mainDock.Views["patch"].Complete {
		t.Fatal("shared completion lost")
	}
	for _, status := range []string{"failed", "declined"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "patch", "type": "fileChange", "status": status, "changes": []any{change}}})
		title := ansi.Strip(u.shell.mainDock.Views["patch"].Title(workspace, u.shell.diff.theme, 120))
		if strings.Contains(title, "✓") || !strings.Contains(title, "· preview") {
			t.Fatalf("%s patch presented as applied: %s", status, title)
		}
	}
	preview.ID, preview.Thread, preview.Caller, preview.Tool = "exec", "child", "/root/worker", nativeExecCommandToolName
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview})
	if len(u.shell.agentDock.Order) != 0 {
		t.Fatal("completion-only shell preview flashed a dock")
	}
	preview.Complete = false
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &preview})
	if len(u.shell.mainDock.Order) != 1 || len(u.shell.agentDock.Order) != 1 {
		t.Fatal("shared previews not docked by caller")
	}
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "turn", TurnRevision: 1, Status: "completed"})
	if u.shell.diffOpen {
		t.Fatal("completed turn opened saved diff")
	}
}

func TestAppServerChangeCounts(t *testing.T) {
	change := appServerFileChange{Diff: "@@\n-x\n+y\n"}
	change.Kind.Type = "update"
	if added, removed := appServerChangeCounts(change); added != 1 || removed != 1 {
		t.Fatalf("counts %d %d", added, removed)
	}
}
