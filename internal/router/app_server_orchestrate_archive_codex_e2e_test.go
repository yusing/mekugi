//go:build journal_e2e

package router

import (
	"slices"
	"testing"
)

func testOrchestrateArchiveNativeCodex(t *testing.T, u *appServerUI, pump func(func() bool)) {
	t.Helper()
	pump(func() bool { return len(u.orchestrateJobs) == 0 && len(u.orchestrateRequests) == 0 })
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, u.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Integrate batch"), State: new("working"), Agent: "/root/batch"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, u.thread, "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	u.flushOrchestratedEvents()
	pump(func() bool {
		for _, child := range u.orchestrateThreads {
			if child.batch.Archive != nil && child.batch.Archive.State == "archived" {
				return true
			}
		}
		return false
	})
	retained, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, u.thread)
	if err != nil || len(retained) != 1 || retained[0].Archive.State != "archived" || retained[0].Integration == nil {
		t.Fatal("native archive lost retained integration", retained, err)
	}
	thread := retained[0].Launch.ThreadID
	n := u.navigation
	n.views[thread].closeOrchestratedView()
	delete(n.views, thread)
	n.order = slices.DeleteFunc(n.order, func(id string) bool { return id == thread })
	n.retained = retained
	var ready bool
	u.resumeOrchestratedBatch("batch", func(err error) {
		if err != nil {
			t.Fatal("read archived native history", err)
		}
		ready = true
	})
	pump(func() bool { return ready })
	v := n.views[thread]
	if len(v.view.entries) == 0 || v.busy() || !v.orchestrateCheckoutUnavailable() || u.viewedUI() != u {
		t.Fatal("native archived history lost its transcript or resumed work")
	}
}
