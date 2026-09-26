package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func restoreContentReply(t *testing.T, u *appServerUI, id int, result any) {
	t.Helper()
	w, err := json.Marshal(map[string]any{"id": id + 1, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	var m appServerMessage
	if err := json.Unmarshal(w, &m); err != nil {
		t.Fatal(err)
	}
	if err := u.message(m); err != nil {
		t.Fatal(err)
	}
}

func restoreContentError(t *testing.T, u *appServerUI, id int) {
	t.Helper()
	w, err := json.Marshal(map[string]any{"id": id + 1, "error": map[string]any{"code": -1, "message": "history unavailable"}})
	if err != nil {
		t.Fatal(err)
	}
	var m appServerMessage
	if err := json.Unmarshal(w, &m); err != nil {
		t.Fatal(err)
	}
	if err := u.message(m); err != nil {
		t.Fatal(err)
	}
}

func restoreContentRequests(t *testing.T, w *appServerTestInput) []struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
} {
	t.Helper()
	var requests []struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(w.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	return requests
}

func TestAppServerRestoreRosterAndActivityFromObservationalHistory(t *testing.T) {
	workspace := t.TempDir()
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", workspace)
	u.resumeThread = "main"
	root := appServerThreadInfo{ID: "main", Cwd: workspace, Turns: []appServerHistoryTurn{{ID: "root-turn", Status: "completed", Items: []appServerItem{
		{ID: "spawn", Type: "collabAgentToolCall", Status: "completed", Tool: "spawnAgent", SenderThreadID: "main", ReceiverThreadIDs: []string{"child"}, Prompt: "Inspect the change."},
		{ID: "followup", Type: "collabAgentToolCall", Status: "completed", Tool: "followupTask", SenderThreadID: "main", ReceiverThreadIDs: []string{"child"}, Prompt: "Check again."},
	}}}}
	if err := u.restorePaneContent(root); err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "live", "agentNickname": "late"}})
	if len(u.resumePending) != 1 || u.session.paths["live"] != "" {
		t.Fatal("live event was applied before history hydration")
	}
	u.draft = "do not send yet"
	appServerTestKeys(t, u, "\r")
	child := map[string]any{"id": "child", "cwd": workspace, "parentThreadId": "main", "agentRole": "review", "createdAt": 100, "updatedAt": 110,
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/reviewer"}}}}
	grand := map[string]any{"id": "grand", "cwd": workspace, "parentThreadId": "child", "agentRole": "explore", "createdAt": 105, "updatedAt": 115,
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/reviewer/explorer"}}}}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{child}, "nextCursor": "page-2"})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}, "nextCursor": nil})
	restoreContentReply(t, u, 2, map[string]any{"data": []any{grand}, "nextCursor": nil})
	requests := restoreContentRequests(t, w)
	if len(requests) != 4 || requests[0].Method != "thread/list" || requests[1].Params["cursor"] != "page-2" || requests[2].Params["archived"] != true || requests[3].Method != "thread/read" || requests[3].Params["includeTurns"] != true || requests[3].Params["threadId"] != "child" {
		t.Fatalf("wrong observational request sequence: %+v", requests)
	}
	restoreContentReply(t, u, 3, map[string]any{"thread": map[string]any{"id": "child", "cwd": workspace, "agentRole": "review", "createdAt": 100, "updatedAt": 110,
		"turns": []any{map[string]any{"id": "c1", "status": "completed", "completedAt": 108, "items": []any{
			map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./internal/router", "exitCode": 2},
			map[string]any{"id": "edit", "type": "fileChange", "changes": []any{map[string]any{"path": "a.go", "diff": "+new line\n", "kind": map[string]any{"type": "add"}}}},
			map[string]any{"id": "message", "type": "collabAgentToolCall", "status": "completed", "tool": "sendMessage", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": "I found a failure."},
			map[string]any{"id": "answer", "type": "agentMessage", "text": "Review complete."},
		}}}}})
	requests = restoreContentRequests(t, w)
	if len(requests) != 5 || requests[4].Method != "thread/read" || requests[4].Params["threadId"] != "grand" {
		t.Fatalf("grandchild was not read: %+v", requests)
	}
	restoreContentReply(t, u, 4, map[string]any{"thread": map[string]any{"id": "grand", "cwd": workspace, "agentRole": "explore", "turns": []any{map[string]any{
		"id": "g1", "status": "inProgress", "items": []any{map[string]any{"id": "partial", "type": "agentMessage", "text": "Partial analysis"}},
	}}}})
	if u.restoring != nil || u.session.paths["live"] != "/root/late" || u.session.agent("/root/reviewer").Role != "review" || u.session.agent("/root/reviewer/explorer").Role != "explore" || u.session.agent("/root/reviewer/explorer").Responding {
		t.Fatalf("hydrated roster or buffered notification missing: %+v", u.session.agents)
	}
	feed := ansi.Strip(strings.Join(u.agents.renderFeed(100, 80).lines, "\n"))
	for _, want := range []string{"Inspect the change.", "Check again.", "go test ./internal/router", "a.go", "I found a failure.", "Review complete.", "Partial analysis", "history only"} {
		if !strings.Contains(feed, want) {
			t.Fatalf("Activity missing %q: %s", want, feed)
		}
	}
	for _, request := range restoreContentRequests(t, w) {
		if request.Method == "thread/resume" || request.Method == "turn/start" || request.Method == "turn/steer" {
			t.Fatalf("observational history triggered execution: %+v", request)
		}
	}
	if len(u.shell.mainDock.order) != 0 || len(u.shell.agentDock.order) != 0 {
		t.Fatal("historical edit reopened a live preview")
	}
}

func TestAppServerRestoreWrongChildIdentityContinues(t *testing.T) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", t.TempDir())
	u.resumeThread = "main"
	if err := u.restorePaneContent(appServerThreadInfo{ID: "main", Cwd: u.session.cwd}); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{map[string]any{"id": "first"}, map[string]any{"id": "second"}}})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}})
	restoreContentReply(t, u, 2, map[string]any{"thread": map[string]any{"id": "wrong"}})
	if len(restoreContentRequests(t, w)) != 4 || restoreContentRequests(t, w)[3].Params["threadId"] != "second" {
		t.Fatal("wrong identity prevented remaining Activity reads")
	}
	restoreContentReply(t, u, 3, map[string]any{"thread": map[string]any{"id": "second", "turns": []any{map[string]any{"id": "t", "status": "completed", "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "Still available"}}}}}})
	if u.restoring != nil || u.session.paths["wrong"] != "" || u.agents.status != "History incomplete" || !strings.Contains(strings.Join(u.agents.renderFeed(90, 50).lines, "\n"), "Still available") {
		t.Fatal("partial failure leaked identity or suppressed later history")
	}
}

func TestAppServerRestoreChildReadErrorContinues(t *testing.T) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", t.TempDir())
	u.resumeThread = "main"
	if err := u.restorePaneContent(appServerThreadInfo{ID: "main", Cwd: u.session.cwd}); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{map[string]any{"id": "first"}, map[string]any{"id": "second"}}})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}})
	restoreContentError(t, u, 2)
	requests := restoreContentRequests(t, w)
	if len(requests) != 4 || requests[3].Params["threadId"] != "second" {
		t.Fatalf("read error stopped remaining history: %+v", requests)
	}
	restoreContentReply(t, u, 3, map[string]any{"thread": map[string]any{"id": "second", "turns": []any{map[string]any{"id": "t", "status": "completed", "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "Recovered answer"}}}}}})
	if u.restoring != nil || u.agents.status != "History incomplete" || !strings.Contains(strings.Join(u.agents.renderFeed(90, 50).lines, "\n"), "Recovered answer") {
		t.Fatal("error hid partial notice or remaining Activity")
	}
}

func TestAppServerRestoreDiffScopeBeforeNewTurn(t *testing.T) {
	workspace, other := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct{ workspace, thread, call, path string }{
		{workspace, "main", "root-edit", filepath.Join(workspace, "root.go")},
		{workspace, "child", "child-edit", filepath.Join(workspace, "child.go")},
		{workspace, "unrelated", "other-thread", filepath.Join(workspace, "unrelated.go")},
		{other, "main", "other-workspace", filepath.Join(other, "foreign.go")},
	} {
		liveDiffScopeCapture(t, store, edit.workspace, edit.thread, edit.call, edit.path, "before", "after")
	}
	auto, stop := newAutoLiveDiff(t.Context(), store.directory)
	defer stop()
	auto.enable()
	u := newAppServerSessionTestUI(t, workspace)
	u.shell.auto = auto
	sub := auto.events.subscribe()
	u.restoreDiffThread(appServerThreadInfo{ID: "main", Cwd: workspace}, true)
	u.restoreDiffThread(appServerThreadInfo{ID: "child", Cwd: workspace}, false)
	var lastScope *liveDiffScope
	for len(sub.events) > 0 {
		event := <-sub.events
		if event.Kind == "scope" {
			lastScope = event.Scope
		}
	}
	if lastScope == nil || !lastScope.Workspaces[workspace]["main"] || !lastScope.Workspaces[workspace]["child"] || lastScope.Workspaces[workspace]["unrelated"] {
		t.Fatalf("native broker missed restored scope: %+v", lastScope)
	}
	reader := &mekugiReplayStore{directory: store.directory}
	files, err := reader.liveDiffSnapshotFiles(t.Context(), auto.scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("restored diff scope included unrelated edits or lost owned edits: %+v", files)
	}
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
	}
	if !paths[filepath.Join(workspace, "root.go")] || !paths[filepath.Join(workspace, "child.go")] {
		t.Fatalf("root or child durable edit absent before provider turn: %+v", paths)
	}
}

func TestAppServerRestoreCollaborationRequiresCompletedStatus(t *testing.T) {
	for _, status := range []string{"inProgress", "failed", "interrupted", ""} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		entries := u.restoredCollab(appServerItem{ID: "send", Type: "collabAgentToolCall", Tool: "sendMessage", Status: status, SenderThreadID: "main", ReceiverThreadIDs: []string{"child"}, Prompt: "undelivered message"}, time.Now())
		if len(entries) != 1 || entries[0].Kind != "error" || !strings.Contains(entries[0].Text, "delivery not confirmed") || strings.Contains(entries[0].Text, "Message sent") {
			t.Fatalf("unfinished delivery presented as successful: %+v", entries)
		}
	}
}

func TestAppServerRestoreListPageBudget(t *testing.T) {
	u, w := newAppServerTestUI()
	u.agents = newLiveActivityView()
	u.session.start("main", t.TempDir())
	if err := u.restorePaneContent(appServerThreadInfo{ID: "main"}); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		restoreContentReply(t, u, i, map[string]any{"data": []any{}, "nextCursor": fmt.Sprint(i + 1)})
	}
	if len(restoreContentRequests(t, w)) != 8 || u.restoring != nil || u.agents.status != "History incomplete" {
		t.Fatalf("list-page budget not enforced before dispatch: %+v", restoreContentRequests(t, w))
	}
}

func TestAppServerRestoreWaitsForDurableDiffBeforeInput(t *testing.T) {
	for _, viaPreviews := range []bool{false, true} {
		t.Run(fmt.Sprint(viaPreviews), func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			liveDiffScopeCapture(t, store, workspace, "main", "edit", filepath.Join(workspace, "restored.go"), "before", "after")
			u := newAppServerSessionTestUI(t, workspace)
			w := u.client.input.(*appServerTestInput)
			auto, stop := newAutoLiveDiff(t.Context(), store.directory)
			defer stop()
			auto.enable()
			u.shell.auto, u.shell.diff.store = auto, store
			sub := auto.events.subscribe()
			if err := u.restorePaneContent(appServerThreadInfo{ID: "main", Cwd: workspace}); err != nil {
				t.Fatal(err)
			}
			restoreContentReply(t, u, 0, map[string]any{"data": []any{}})
			restoreContentReply(t, u, 1, map[string]any{"data": []any{}})
			u.draft = "next turn"
			appServerTestKeys(t, u, "\r")
			if u.restoring == nil || len(u.shell.diff.data.files()) != 0 || len(restoreContentRequests(t, w)) != 2 {
				t.Fatal("input unlocked before the Diff controller consumed the restored scope")
			}
			if viaPreviews {
				for _, event := range auto.events.takePreviews(sub) {
					u.shell.applyDiff(t.Context(), event)
				}
				if err := u.finishRestoredContent(); err != nil {
					t.Fatal(err)
				}
			} else {
				for len(sub.events) > 0 {
					u.shell.applyDiff(t.Context(), <-sub.events)
					if err := u.finishRestoredContent(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if u.restoring != nil || len(u.shell.diff.data.files()) != 1 || u.shell.diffFailure != "" {
				t.Fatalf("durable Diff not hydrated: %s", u.shell.diffFailure)
			}
			appServerTestKeys(t, u, "\r")
			requests := restoreContentRequests(t, w)
			if len(requests) != 3 || requests[2].Method != "turn/start" {
				t.Fatal("input did not unlock after hydration")
			}

		})
	}
}
