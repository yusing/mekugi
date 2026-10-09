package router

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalCounterWritesPreserveTaskTiming(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "durable"}[durable], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy, workspace := treeTestJournal(t)
				if !durable {
					proxy.replayStore = nil
					if err := proxy.journals.initialize(t.Context(), nil, workspace, "tree", "/root", ""); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := func() threadJournal {
					if durable {
						return treeSnapshot(t, proxy, workspace)
					}
					return proxy.journals.memory[journalKey(workspace, "tree")].clone()
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("working")})
				before := snapshot()
				time.Sleep(3 * time.Second)
				proxy.countJournalRead(t.Context(), workspace, "tree", "read-call", "read")
				proxy.countJournalRead(t.Context(), workspace, "tree", "read-call", "read")
				proxy.journalCounters(t.Context(), workspace, "tree", "", nil)
				after := snapshot()
				if after.Counters.Operations["read"] != 1 {
					t.Fatalf("counter receipt lost: %+v", after.Counters)
				}
				// Only the measurements and their bounded receipts may change.
				after.Counters, after.CounterReceipts = before.Counters, before.CounterReceipts
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("counter changed journal state:\nbefore %+v\nafter %+v", before, after)
				}
				if got := after.Items[0].WorkTimer.at(time.Now()); got != 3*time.Second {
					t.Fatalf("counter lost live elapsed time: %v", got)
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")})
				if got := journalTaskElapsed(snapshot().Items[0].node()); got != "3s" {
					t.Fatalf("work transaction lost elapsed time: %q", got)
				}
			})
		})
	}
}
