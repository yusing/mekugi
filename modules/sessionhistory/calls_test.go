package sessionhistory_test

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestComposedCallCachePreservesOrderPinsAndAtomicAdmission(t *testing.T) {
	service := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, nil)
	cache := service.Calls()
	cache.Pin("pinned")
	initial := make([]history.Call, 128)
	for i := range initial {
		initial[i] = history.Call{ID: fmt.Sprint(i), History: legacycall.History{Script: fmt.Sprint(i)}}
	}
	if err := cache.Remember("pinned", initial); err != nil {
		t.Fatal(err)
	}
	if err := cache.Remember("pinned", []history.Call{{ID: "0", History: legacycall.History{Script: "updated"}}, {ID: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, exists := cache.Lookup("pinned", "1"); exists {
		t.Fatal("protected replacement displaced the wrong call")
	}
	if retained, exists := cache.Lookup("pinned", "0"); !exists || retained.Script != "updated" {
		t.Fatal("replacement was evicted")
	}
	if err := cache.Remember("pinned", append(initial, history.Call{ID: "overflow"})); err == nil {
		t.Fatal("oversized batch admitted")
	}
	if retained, _ := cache.Lookup("pinned", "0"); retained.Script != "updated" {
		t.Fatal("rejected batch changed the cache")
	}
	for i := range 256 {
		if err := cache.Remember(fmt.Sprint(i), []history.Call{{ID: "call"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, exists := cache.Lookup("pinned", "0"); !exists {
		t.Fatal("active session evicted")
	}
	cache.Unpin("pinned")
	if err := cache.Remember("last", []history.Call{{ID: "call"}}); err != nil {
		t.Fatal(err)
	}
	if _, exists := cache.Lookup("pinned", "0"); exists {
		t.Fatal("old unpinned session retained under capacity pressure")
	}
	cache.Clear()
	if _, exists := cache.Lookup("last", "call"); exists {
		t.Fatal("invocation shutdown retained call cache")
	}
}
