package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestAppServerInterruptedWaitEndsBeforeContinue(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.session.registerThread(binding.ThreadInfo{ID: "child"})
			wait := binding.ThreadItem{ID: "wait", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress", ReceiverThreadIDs: []string{"target"}}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn"}})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "turn", "item": wait})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn", "status": "interrupted"}})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "target", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "continue"}})
			view := u.view
			if thread != "main" {
				view = u.agents
			}
			if len(view.Entries) != 1 || view.Entries[0].Source.Text != "Wait ended · target" || view.Entries[0].Source.native.phase != "turn/completed" {
				t.Fatalf("interrupted wait survived continue: %+v", view.Entries)
			}
			// A late real item result replaces the display-only turn closure.
			wait.Status = "completed"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": wait})
			if !strings.HasPrefix(view.Entries[0].Source.Text, "Finished waiting") {
				t.Fatal("late host result was lost")
			}
		})
	}
}

func TestAppServerRestoredStoppedWait(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			history := []binding.HistoryTurn{{ID: "old", Status: status, Items: []binding.ThreadItem{{ID: "wait", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress"}}}}
			u.restoreHistory(history)
			u.session.registerThread(binding.ThreadInfo{ID: "child"})
			u.restoreActivityThread(binding.ThreadInfo{ID: "child", Turns: history}, false)
			for _, view := range []*activityAdapter{u.view, u.agents} {
				if view.Entries[0].Source.Text != "Wait ended" || view.Entries[0].Source.native.phase != "turn/completed" {
					t.Fatalf("stopped history revived waiting: %+v", view.Entries[0])
				}
			}
		})
	}
}

func TestAppServerInterruptedLiveDiffCannotReopen(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	auto := newLivePreview(t.Context())
	defer auto.Close()
	setTestAppServerIntegration(u, &mekugiProxy{review: testReview(auto), execWindows: &execWindowRegistry{}})
	workspace := u.session.cwd
	setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}})
	u.shell.saved.scope = testReview(auto).Scope()
	sub := testReview(auto).Subscribe()
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	main := previewmodel.Preview{ID: "edit", Workspace: workspace, Thread: "main", Turn: "turn", Caller: "/root", Status: previewmodel.PreviewRunning, Input: "observed edit"}
	child := main
	child.ID, child.Thread, child.Caller = "child-edit", "child", "/root/child"
	for _, preview := range []previewmodel.Preview{main, child} {
		auto.Update(preview, false)
		u.shell.preview(preview, false)
		revealNativeDock(u.shell)
	}
	queued := main
	queued.ID, queued.Complete, queued.Evaluated, queued.Tool = "queued-final", true, true, applyPatchToolName
	auto.Update(queued, false)
	canceled := false
	u.execWindows.open(&execWindow{ref: "cell", thread: "main", turn: "turn", previewCancel: func() { canceled = true }})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	if !canceled || u.shell.liveDock.Views[main.ID] != nil || u.shell.liveDock.Views[child.ID] == nil {
		t.Fatal("interrupt did not retire only its own live preview")
	}
	if !u.execWindows.find("cell").closed.IsZero() {
		t.Fatal("display cleanup finalized capture")
	}
	// An asynchronous worker can race with interruption and the next turn.
	for _, active := range []bool{false, true} {
		if active {
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "next"}})
		}
		auto.Update(main, false)
		for _, event := range testReview(auto).TakePreviews(sub) {
			if event.Kind == "preview" {
				u.shell.applyDiff(t.Context(), event)
			}
		}
		if u.shell.liveDock.Views[main.ID] != nil || u.shell.liveDock.Views[queued.ID] != nil {
			t.Fatal("late producer or queued completion reopened interrupted preview")
		}
	}
	sub = testReview(auto).Subscribe()
	for _, event := range testReview(auto).TakePreviews(sub) {
		if event.Preview != nil && event.Preview.Thread == "main" && event.Preview.Status != "" {
			t.Fatal("resubscription revived stale preview")
		}
	}
	if u.shell.liveDock.Views[child.ID] == nil {
		t.Fatal("child preview was retired with main")
	}
}
