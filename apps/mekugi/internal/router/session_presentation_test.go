package router

import (
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func TestNonTerminalDisplayPublishesWithoutActivityOrPaint(t *testing.T) {
	for _, activity := range []bool{true, false} {
		t.Run(map[bool]string{true: "composed", false: "without Activity"}[activity], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if !activity {
				u.session.activity = nil
			}
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			editOrdinaryInput(t, u, "Main draft")
			before := u.sessionPresentation().View()
			if !u.displaySession(sessionpresentation.DisplayIntent{Version: before.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}) {
				t.Fatal(u.feedback.Text)
			}
			view := u.sessionPresentation().View()
			if view.ReadOnly != sessionpresentation.ChildDisplayed || view.Status.Composer.Text != "Read-only" || view.Input != before.Input || view.Draft.Draft.Text != "Main draft" {
				t.Fatalf("display did not publish a complete read-only view: %+v", view)
			}
			if u.displaySession(sessionpresentation.DisplayIntent{Version: before.Version, Thread: before.Main}) || u.sessionPresentation().View().Displayed.ID != "child" {
				t.Fatal("stale display changed the selected thread")
			}
			view = u.sessionPresentation().View()
			if !u.displaySession(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main}) || u.sessionPresentation().View().ReadOnly != sessionpresentation.Editable || u.Text != "Main draft" {
				t.Fatal("Main did not regain its retained draft")
			}
		})
	}
}

func TestSessionPaintDoesNotRefreshPublication(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Unix(100, 0)
	u.clock = func() time.Time { return now }
	appServerTestTurn(t, u, "turn")
	questionTestSync(t, u, "request", false)
	before := u.sessionPresentation().View()
	if before.Questions.Call == nil || u.questions.painted {
		t.Fatal("prompt publication did not precede its display receipt")
	}
	now = now.Add(time.Second)
	for range 2 {
		if err := u.paint(io.Discard, 100, 24); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(u.sessionPresentation().View(), before) {
			t.Fatal("paint refreshed the publication or changed its version")
		}
	}
	if !u.questions.painted {
		t.Fatal("successful visible delivery did not acknowledge the prompt")
	}
}

// Tests that arrange a feed filter also retain the renderer's remembered row.
// Production selection enters through roster keys, clicks and Main selection.
func (v *activityAdapter) setDisplayFilter(child bool, caller string) {
	v.Selected = caller
	if child {
		v.displayThread(caller)
	} else {
		v.displayThread("/root")
	}
}

func TestNativeDisplayedThreadIndependentOfRosterFocus(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	for _, thread := range []string{"first", "second"} {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": thread}})
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "work"}})
	}
	first := u.session.Transcript("first", "work").Turn
	u.testActivity(u.agents).displayThread("/root/first")
	u.agents.Selected = "/root/second"
	u.testActivity(u.agents).testKeepSelection()
	view := u.sessionPresentation().View()
	if view.Displayed.ID != "first" || view.Input.ID != "main" || !u.composerReadOnly() || u.transcriptCaller() != "/root/first" {
		t.Fatalf("roster focus changed display or admission: %+v", view)
	}
	if !u.testActivity(u.agents).testVisible(sessionpresentation.ActivityEntry{Agent: "/root/first", Kind: "text"}) || u.testActivity(u.agents).testVisible(sessionpresentation.ActivityEntry{Agent: "/root/second", Kind: "text"}) {
		t.Fatal("transcript followed the focused roster row")
	}
	u.shell.selectNativePane(0)
	if u.composerReadOnly() || u.transcriptView() != u.view || u.agents.Selected != "/root/second" {
		t.Fatal("Main selection changed roster focus or retained child admission")
	}
	if first == nil || u.session.Transcript("first", "work").Turn != first || !u.session.agent("/root/first").Responding {
		t.Fatal("display switching retired a child's work scope")
	}
}

func TestNativeDisplaySwitchKeepsHistoryObservation(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	u.testActivity(u.agents).displayThread("/root/worker")
	if err := u.loadOlderActivity(historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	id := u.historyLoading().state.RequestID()
	u.shell.selectNativePane(0)
	if u.historyLoading() == nil || u.historyLoading().state.RequestID() != id || len(restoreContentRequests(t, wire)) != 6 {
		t.Fatal("display switching retired the observation or dispatched host work")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "older", 101000)}})
	if u.historyLoading() != nil || u.sessionPresentation().View().ReadOnly != sessionpresentation.Editable {
		t.Fatal("background history settlement changed Main admission")
	}
}

func (v *activityAdapter) displayThread(caller string) {
	if caller != "" && caller != "/root" {
		v.Selected = caller
	}
	_ = v.display.Display(sessionpresentation.DisplayIntent{Version: v.display.View().Version, Thread: v.displayTarget(caller)})
	v.publishActivity()
}
func (v *activityAdapter) applyDisplayIntent() {
	if intent := v.takeDisplayIntent(); intent != nil {
		if err := v.display.Display(*intent); err == nil && intent.Thread.Caller != "/root" {
			v.Selected = intent.Thread.Caller
		}
		v.publishActivity()
	}
}
