package sessionpresentation_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestNonTerminalDisplayAdmissionAndAttachment(t *testing.T) {
	var service sessionpresentation.Service
	var store scope.Store
	m := service.Session(&store)
	main := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	child := sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}
	m.Attach(main, false)
	before := m.View()
	intent := sessionpresentation.DisplayIntent{Version: before.Version, Thread: child}
	if err := m.Display(intent); err != nil {
		t.Fatal(err)
	}
	view := m.View()
	if view.Input != main || view.Displayed != child || view.ReadOnly != sessionpresentation.ChildDisplayed || before.Displayed != main {
		t.Fatalf("display changed input identity or mutated a detached view: %+v", view)
	}
	if err := m.Display(intent); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("stale display: %v", err)
	}
	m.RenameCaller(child.Caller, "/root/renamed")
	if got := m.View().Displayed; got.ID != child.ID || got.Caller != "/root/renamed" {
		t.Fatal(got)
	}
	renamed := m.View()
	m.RenameCaller("/root/renamed", "/root/renamed")
	if m.View() != renamed {
		t.Fatal("unchanged metadata retired the current view")
	}
	if err := m.Display(sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: main}); err != nil {
		t.Fatal(err)
	}
	if m.View().ReadOnly != sessionpresentation.Editable {
		t.Fatal("Main stayed read-only")
	}
	intent = sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: child}
	store = scope.Store{}
	m = service.Session(&store)
	m.Attach(main, false)
	if err := m.Display(intent); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("retired attachment: %v", err)
	}
	m.Attach(main, true)
	if m.View().ReadOnly != sessionpresentation.Replay || m.View().Input != main {
		t.Fatal("replay admitted input")
	}
}

func TestNonTerminalHistoryPagingRetryCancellation(t *testing.T) {
	var service sessionpresentation.Service
	var session, thread scope.Store
	m := service.Session(&session)
	m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, false)
	h := service.History(&thread)
	request := m.StartHistory(h, "child")
	if request.Kind != sessionpresentation.TurnMetadata {
		t.Fatal(request)
	}
	h.BindRequest("metadata")
	if h.AcceptResponse("foreign") || !h.AcceptResponse("metadata") || h.AcceptResponse("metadata") {
		t.Fatal("response identity guard")
	}
	page, err := h.Turns([]string{"new", "old", "old"}, "")
	if err != nil || !page.Complete || !slices.Equal(page.Accepted, []int{0, 1}) || page.Next.Turn != "new" {
		t.Fatalf("turn metadata: %+v %v", page, err)
	}
	items, err := h.Items([]sessionpresentation.ItemIdentity{{Turn: "new", Item: "last"}, {Turn: "new", Item: "middle"}}, "older")
	if err != nil || !slices.Equal(items.Accepted, []int{1, 0}) || items.Older || items.Exhausted {
		t.Fatalf("initial items: %+v %v", items, err)
	}
	if !m.FinishHistory(h) {
		t.Fatal("initial history did not settle")
	}
	load := func() sessionpresentation.PageRequest {
		t.Helper()
		request, err := m.LoadHistory(sessionpresentation.HistoryIntent{Version: m.View().Version, Thread: "child"}, h)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	request = load()
	if request.Anchor != "middle" || request.Cursor != "" || request.Turn != "new" {
		t.Fatal(request)
	}
	if h.BufferEvent("other", false, 256) != sessionpresentation.Deliver || h.BufferEvent("child", true, 256) != sessionpresentation.DiscardDelta || h.BufferEvent("child", false, 255) != sessionpresentation.Buffer || h.BufferEvent("child", false, 256) != sessionpresentation.CancelRead {
		t.Fatal("live buffering decision")
	}
	h.BindRequest("cancelled")
	id, err := m.CancelHistory(sessionpresentation.HistoryIntent{Version: m.View().Version, Thread: "child"}, h)
	if err != nil || id != "cancelled" || h.AcceptResponse(id) {
		t.Fatalf("cancel: %q %v", id, err)
	}
	m.FinishHistory(h)
	if retry := load(); retry != request {
		t.Fatalf("cancel lost anchor: %+v", retry)
	}
	if _, err := h.Items([]sessionpresentation.ItemIdentity{{Turn: "foreign", Item: "leak"}}, ""); err == nil {
		t.Fatal("foreign item admitted")
	}
	h.Fail("read failed")
	m.FinishHistory(h)
	if h.View("").Status != sessionpresentation.Failed {
		t.Fatal("failure hidden")
	}
	if retry := load(); retry != request {
		t.Fatalf("failure lost retry: %+v", retry)
	}
	h.ObserveLive()
	items, err = h.Items([]sessionpresentation.ItemIdentity{{Turn: "new", Item: "middle"}, {Turn: "new", Item: "first"}}, "")
	if err != nil || !items.Older || !slices.Equal(items.Accepted, []int{1}) || !h.View("").Live {
		t.Fatalf("older reconciliation: %+v %v", items, err)
	}
	m.FinishHistory(h)
	if request = load(); request.Turn != "old" || request.Anchor != "" {
		t.Fatal(request)
	}
	if _, err := h.Items(nil, ""); err != nil {
		t.Fatal(err)
	}
	m.FinishHistory(h)
	if h.Available() || h.View("").Status != sessionpresentation.Exhausted {
		t.Fatal("exhausted history stayed available")
	}
}
