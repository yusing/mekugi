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
)

func appServerTestNotify(t *testing.T, u *appServerUI, method string, params any) {
	t.Helper()
	wire, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.message(appServerMessage{Method: method, Params: jsontext.Value(wire)}); err != nil {
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
	for _, want := range []string{"worker · explore", "▶ started · gpt-6-luna", "Inspect the pane.", "├ Run    go test ./... (exit 2)", "└ Read", "✓ answer", "The pane waits on its first frame."} {
		if !strings.Contains(activity, want) {
			t.Fatalf("Activity lacks %q:\n%s", want, activity)
		}
	}
	u.view.conversation = true
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
	for _, want := range []string{"▶ worker started", "✓ worker finished", "↩ re: assignment", "The pane waits on its first frame."} {
		if !strings.Contains(main, want) {
			t.Fatalf("Main lacks %q:\n%s", want, main)
		}
	}
	if strings.Contains(main, "go test") {
		t.Fatal("a child's command reached Main")
	}
}

func TestAppServerSessionDocksEditsByCaller(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	update := map[string]any{"path": filepath.Join(workspace, "a.go"), "kind": map[string]any{"type": "update"}, "diff": "@@\n func A() {}\n+\n+func B() {}\n"}
	appServerTestNotify(t, u, "item/fileChange/patchUpdated", map[string]any{"threadId": "main", "turnId": "t", "itemId": "p1", "changes": []any{update}})
	appServerTestNotify(t, u, "item/fileChange/patchUpdated", map[string]any{"threadId": "child", "turnId": "c", "itemId": "p2",
		"changes": []any{map[string]any{"path": "b.go", "kind": map[string]any{"type": "add"}, "diff": "package a\n"}}})
	if len(u.shell.mainDock.order) != 1 || len(u.shell.agentDock.order) != 1 {
		t.Fatalf("edits were not docked by caller: main %d, agents %d", len(u.shell.mainDock.order), len(u.shell.agentDock.order))
	}
	card := u.shell.mainDock.views[u.shell.mainDock.order[0]].current
	if len(card.Files) != 1 || !strings.Contains(card.Files[0].Diff, "+func B() {}") || card.Complete {
		t.Fatalf("streamed patch was not projected: %+v", card)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "p1", "type": "fileChange", "status": "completed", "changes": []any{update}}})
	if !u.shell.mainDock.views[u.shell.mainDock.order[0]].complete {
		t.Fatal("applied patch left its card streaming")
	}
	// The router's prediction of the same apply_patch call is a duplicate;
	// an exec edit is not.
	u.shell.diff.scope = liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}}
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &liveDiffPreview{ID: "router", Workspace: workspace, Thread: "main", Caller: "/root", Tool: applyPatchToolName, Status: liveDiffPreviewEdit, Input: "x"}})
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "preview", Preview: &liveDiffPreview{ID: "exec", Workspace: workspace, Thread: "main", Caller: "/root", Tool: nativeExecCommandToolName, Status: liveDiffPreviewEdit, Input: "x"}})
	if u.shell.mainDock.views["router"] != nil || u.shell.mainDock.views["exec"] == nil {
		t.Fatal("router previews were not filtered to non-app-server tools")
	}
	// A completed turn never swaps Activity for the saved diff.
	u.shell.applyDiff(t.Context(), liveDiffEvent{Kind: "turn", TurnRevision: 1, Status: "completed"})
	if u.shell.diffOpen {
		t.Fatal("a completed turn opened the saved diff")
	}
}

func TestAppServerPatchText(t *testing.T) {
	changes := []appServerFileChange{
		{Path: "/w/new.go", Diff: "package a\n"},
		{Path: "old.go", Diff: "gone\n"},
		{Path: "/w/a.go", Diff: "@@\n-x\n+y\n"},
	}
	changes[0].Kind.Type, changes[1].Kind.Type, changes[2].Kind.Type = "add", "delete", "update"
	changes[2].Kind.MovePath = "/w/b.go"
	want := "*** Begin Patch\n*** Add File: new.go\n+package a\n*** Delete File: old.go\n*** Update File: a.go\n*** Move to: b.go\n@@\n-x\n+y\n*** End Patch\n"
	if got := appServerPatchText(changes, "/w", true); got != want {
		t.Fatalf("patch text:\n%s\nwant:\n%s", got, want)
	}
	if added, removed := appServerChangeCounts(changes[2]); added != 1 || removed != 1 {
		t.Fatalf("counts %d %d", added, removed)
	}
}
