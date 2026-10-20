package sessionhistory_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestComposedCallRequestsProtectHandoffsAndShutdown(t *testing.T) {
	cache := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, nil).Calls()
	begin := func(session, thread string) *history.CallRequest {
		t.Helper()
		r, err := cache.Begin(session, thread)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	bind := func(r *history.CallRequest, release func()) {
		t.Helper()
		if err := r.BindLease(release); err != nil {
			t.Fatal(err)
		}
	}
	var released, redundant, other int
	old := begin("old workspace key", "thread")
	bind(old, func() { released++ })
	newer := begin("new workspace key", "thread")
	bind(newer, func() { redundant++ })
	newer.Finish(false)
	old.Finish(true)
	if released != 0 || redundant != 1 {
		t.Fatal("old completion released a newer handoff")
	}
	latest := begin("latest workspace key", "thread")
	bind(latest, func() { redundant++ })
	unrelated := begin("unrelated key ending in thread", "other")
	bind(unrelated, func() { other++ })
	latest.Finish(true)
	latest.Finish(true)
	if released != 1 || redundant != 2 || other != 0 {
		t.Fatal("explicit thread identity did not isolate idle release")
	}
	cache.StopAdmission()
	if err := cache.Remember("unrelated key ending in thread", []history.Call{{ID: "accepted observation"}}); err != nil {
		t.Fatal("observer drain rejected already accepted facts:", err)
	}
	unrelated.Finish(true)
	if other != 0 {
		t.Fatal("lease ended before application observers drained")
	}
	if _, err := cache.Begin("late", "other"); err == nil {
		t.Fatal("shutdown admitted new work")
	}
	cache.Close()
	cache.Close()
	if other != 1 {
		t.Fatal("shutdown did not release the remaining lease exactly once")
	}
	if err := cache.Remember("late", []history.Call{{ID: "call"}}); err == nil {
		t.Fatal("late observation revived a closed invocation")
	}
}

func TestCallRequestLeaseAcquisitionRacesShutdown(t *testing.T) {
	for range 50 {
		cache := new(history.CallCache)
		request, err := cache.Begin("session", "thread")
		if err != nil {
			t.Fatal(err)
		}
		var released atomic.Int32
		done := make(chan struct{})
		go func() {
			_ = request.BindLease(func() { released.Add(1) })
			close(done)
		}()
		cache.StopAdmission()
		cache.Close()
		<-done
		request.Finish(true)
		if released.Load() != 1 {
			t.Fatal("racing acquisition was leaked or released twice")
		}
	}
}

func TestComposedCallCachePreservesOrderPinsAndAtomicAdmission(t *testing.T) {
	service := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, nil)
	cache := service.Calls()
	request, err := cache.Begin("pinned", "thread")
	if err != nil {
		t.Fatal(err)
	}
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
	request.Finish(false)
	if err := cache.Remember("last", []history.Call{{ID: "call"}}); err != nil {
		t.Fatal(err)
	}
	if _, exists := cache.Lookup("pinned", "0"); exists {
		t.Fatal("old unpinned session retained under capacity pressure")
	}
	cache.Close()
	if _, exists := cache.Lookup("last", "call"); exists {
		t.Fatal("invocation shutdown retained call cache")
	}
}
