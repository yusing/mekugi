package router

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

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
	}
	u.agents.displayThread("/root/first")
	u.agents.Selected = "/root/second"
	u.agents.testKeepSelection()
	view := u.sessionPresentation().View()
	if view.Displayed.ID != "first" || view.Input.ID != "main" || !u.composerReadOnly() || u.transcriptCaller() != "/root/first" {
		t.Fatalf("roster focus changed display or admission: %+v", view)
	}
	if !u.agents.testVisible(activityPaneEntry{Agent: "/root/first", Kind: "text"}) || u.agents.testVisible(activityPaneEntry{Agent: "/root/second", Kind: "text"}) {
		t.Fatal("transcript followed the focused roster row")
	}
	u.shell.selectNativePane(0)
	if u.composerReadOnly() || u.transcriptView() != u.view || u.agents.Selected != "/root/second" {
		t.Fatal("Main selection changed roster focus or retained child admission")
	}
}

func TestNativeDisplaySwitchKeepsHistoryObservation(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	u.agents.displayThread("/root/worker")
	if err := u.loadOlderActivity(); err != nil {
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
