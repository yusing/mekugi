package sessionpresentation_test

import (
	"errors"
	"reflect"
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
	if err := m.Display(intent); err != nil {
		t.Fatalf("same target was invalidated by display publication: %v", err)
	}
	m.RenameCaller(child.Caller, "/root/renamed")
	if got := m.View().Displayed; got.ID != child.ID || got.Caller != "/root/renamed" {
		t.Fatal(got)
	}
	renamed := m.View()
	m.RenameCaller("/root/renamed", "/root/renamed")
	if !reflect.DeepEqual(m.View(), renamed) {
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
