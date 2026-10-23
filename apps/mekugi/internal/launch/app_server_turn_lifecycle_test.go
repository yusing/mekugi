package launch

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestAppServerInterruptedLiveDiffCannotReopen(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())

	defer terminalTest(u).Diff.Close()
	auto := newLivePreview(t.Context())
	defer auto.Close()
	setTestAppServerIntegration(u, &observationFixture{review: testReview(auto), execWindows: &executionobservation.Windows{}})
	workspace := u.attached.cwd
	setLiveScope(auto, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}})
	u.review.Projection.Scope = testReview(auto).Scope()
	sub := testReview(auto).Subscribe()
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	main := previewmodel.Preview{ID: "edit", Workspace: workspace, Thread: "main", Turn: "turn", Caller: "/root", Status: previewmodel.PreviewRunning, Input: "observed edit"}
	child := main
	child.ID, child.Thread, child.Caller = "child-edit", "child", "/root/child"
	for _, preview := range []previewmodel.Preview{main, child} {
		auto.Update(preview, false)
		applyPreview(&u.attached, u.shell, u.review, &u.pendingEditReceipts, presentationTime(u.clock), preview, false)
		revealNativeDock(terminalTest(u))
	}
	queued := main
	queued.ID, queued.Complete, queued.Evaluated, queued.Tool = "queued-final", true, true, applyPatchToolName
	auto.Update(queued, false)
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	if terminalTest(u).Live.Views[main.ID] != nil || terminalTest(u).Live.Views[child.ID] == nil {
		t.Fatal("interrupt did not retire only its own live preview")
	}
	// An asynchronous worker can race with interruption and the next turn.
	for _, active := range []bool{false, true} {
		if active {
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "next"}})
		}
		auto.Update(main, false)
		for _, event := range testReview(auto).TakePreviews(sub) {
			if event.Kind == "preview" {
				applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), event)
			}
		}
		if terminalTest(u).Live.Views[main.ID] != nil || terminalTest(u).Live.Views[queued.ID] != nil {
			t.Fatal("late producer or queued completion reopened interrupted preview")
		}
	}
	sub = testReview(auto).Subscribe()
	for _, event := range testReview(auto).TakePreviews(sub) {
		if event.Preview != nil && event.Preview.Thread == "main" && event.Preview.Status != "" {
			t.Fatal("resubscription revived stale preview")
		}
	}
	if terminalTest(u).Live.Views[child.ID] == nil {
		t.Fatal("child preview was retired with main")
	}
}
