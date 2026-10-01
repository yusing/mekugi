package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

func TestAppServerInterruptedWaitEndsBeforeContinue(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.session.registerThread(appServerThreadInfo{ID: "child"})
			wait := appServerItem{ID: "wait", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress", ReceiverThreadIDs: []string{"target"}}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn"}})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "turn", "item": wait})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn", "status": "interrupted"}})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "target", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "continue"}})
			view := u.view
			if thread != "main" {
				view = u.agents
			}
			if len(view.entries) != 1 || view.entries[0].Text != "Wait ended · target" || view.entries[0].native.phase != "turn/completed" {
				t.Fatalf("interrupted wait survived continue: %+v", view.entries)
			}
			// A late real item result replaces the display-only turn closure.
			wait.Status = "completed"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": wait})
			if !strings.HasPrefix(view.entries[0].Text, "Finished waiting") {
				t.Fatal("late host result was lost")
			}
		})
	}
}

func TestAppServerRestoredStoppedWait(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			history := []appServerHistoryTurn{{ID: "old", Status: status, Items: []appServerItem{{ID: "wait", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress"}}}}
			u.restoreHistory(history)
			u.session.registerThread(appServerThreadInfo{ID: "child"})
			u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: history})
			for _, view := range []*liveActivityView{u.view, u.agents} {
				if view.entries[0].Text != "Wait ended" || view.entries[0].native.phase != "turn/completed" {
					t.Fatalf("stopped history revived waiting: %+v", view.entries[0])
				}
			}
		})
	}
}

func TestAppServerInterruptedLiveDiffCannotReopen(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	auto, stop := newAutoLiveDiff(t.Context(), "")
	defer stop()
	u.proxy = &mekugiProxy{autoLiveDiff: auto, execWindows: &execWindowRegistry{}}
	workspace := u.session.cwd
	auto.events.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}})
	u.shell.diff.scope = cloneLiveDiffScope(auto.events.scope)
	sub := auto.events.subscribe()
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	main := diffview.Preview{ID: "edit", Workspace: workspace, Thread: "main", Turn: "turn", Caller: "/root", Status: diffview.PreviewRunning, Input: "observed edit"}
	child := main
	child.ID, child.Thread, child.Caller = "child-edit", "child", "/root/child"
	for _, preview := range []diffview.Preview{main, child} {
		auto.events.publishPreview(preview, false)
		u.shell.preview(preview)
	}
	queued := main
	queued.ID, queued.Complete, queued.Evaluated, queued.Tool = "queued-final", true, true, applyPatchToolName
	auto.events.publishPreview(queued, false)
	canceled := false
	u.proxy.execWindows.open(&execWindow{ref: "cell", thread: "main", turn: "turn", previewCancel: func() { canceled = true }})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	if !canceled || u.shell.liveDock.Views[main.ID] != nil || u.shell.liveDock.Views[child.ID] == nil {
		t.Fatal("interrupt did not retire only its own live preview")
	}
	if !u.proxy.execWindows.find("cell").closed.IsZero() {
		t.Fatal("display cleanup finalized capture")
	}
	// An asynchronous worker can race with interruption and the next turn.
	for _, active := range []bool{false, true} {
		if active {
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "next"}})
		}
		auto.events.publishPreview(main, false)
		for _, event := range auto.events.takePreviews(sub) {
			if event.Kind == "preview" {
				u.shell.applyNativeDiff(t.Context(), event)
			}
		}
		if u.shell.liveDock.Views[main.ID] != nil || u.shell.liveDock.Views[queued.ID] != nil {
			t.Fatal("late producer or queued completion reopened interrupted preview")
		}
	}
	sub = auto.events.subscribe()
	for _, event := range auto.events.takePreviews(sub) {
		if event.Preview != nil && event.Preview.Thread == "main" && event.Preview.Status != "" {
			t.Fatal("resubscription revived stale preview")
		}
	}
	if u.shell.liveDock.Views[child.ID] == nil {
		t.Fatal("child preview was retired with main")
	}
}
