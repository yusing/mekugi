package control

import (
	"fmt"
	"testing"
)

func TestInterruptionsCorrelateWorkWithoutParentDelivery(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	child(t, s, "a", "b", "/root/a/b", "bt")
	caller := identity("root", "rt")
	caller.Call = "interrupt-b"
	action, err := s.PrepareAction(t.Context(), caller, "/root/a/b", "interrupt", "")
	must(t, err)
	_, err = s.ClaimEffect(t.Context(), "root", action.ID)
	must(t, err)
	must(t, s.EffectAcknowledged(t.Context(), "root", action.ID, Receipt{}))
	before := s.Interruptions("root", nil)
	if before.Outcome("root", action.ID) != "" {
		t.Fatal("acknowledgement became completion")
	}
	must(t, s.HostCompleted(t.Context(), "b", "bt", Interrupted, HostFailure{}))
	after := s.Interruptions("root", before)
	if after.Outcome("root", action.ID) != Interrupted || before.Outcome("root", action.ID) != "" {
		t.Fatal("non-parent outcome missing or publication mutated")
	}
	if got := s.Interruptions("root", after); got != after {
		t.Fatal("unchanged state rebuilt projection")
	}
	caller.Call = "restart"
	restart, err := s.PrepareAction(t.Context(), caller, "/root/a/b", "followup", "again")
	must(t, err)
	dispatch, err := s.ClaimEffect(t.Context(), "root", restart.ID)
	must(t, err)
	if dispatch == nil || dispatch.Generation != action.Generation+1 {
		t.Fatalf("new work was not dispatched: %+v", dispatch)
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", restart.ID, Receipt{Turn: "bt-new"}))
	priorID := action.ID
	caller.Call = "interrupt-new"
	action, err = s.PrepareAction(t.Context(), caller, "/root/a/b", "interrupt", "")
	must(t, err)
	dispatch, err = s.ClaimEffect(t.Context(), "root", action.ID)
	must(t, err)
	if dispatch == nil || dispatch.Turn != "bt-new" || dispatch.Generation != restart.Generation {
		t.Fatalf("new interruption was not dispatched: %+v", dispatch)
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", action.ID, Receipt{}))
	publication := s.Interruptions("root", after)
	if publication.Outcome("root", action.ID) != "" {
		t.Fatal("old generation settled new interruption")
	}
	must(t, s.HostCompleted(t.Context(), "b", "bt", Interrupted, HostFailure{}))
	if got := s.Interruptions("root", publication); got != publication || got.Outcome("root", action.ID) != "" {
		t.Fatal("delayed old terminal changed the new interruption")
	}
	must(t, s.HostCompleted(t.Context(), "b", "bt-new", Failed, HostFailure{Message: "host failed"}))
	settled := s.Interruptions("root", publication)
	if settled.Outcome("root", action.ID) != Failed || settled.Outcome("root", priorID) != Interrupted || publication.Outcome("root", action.ID) != "" {
		t.Fatal("current terminal failed to settle only its generation immutably")
	}

	restored := New()
	must(t, restored.Restore(t.Context(), session))
	recovered := restored.Interruptions("root", nil)
	if recovered.Outcome("root", priorID) != Interrupted || recovered.Outcome("root", action.ID) != Failed {
		t.Fatal("checkpoint did not restore exact outcomes before any request or parent delivery")
	}
}

func TestInterruptionsUnchangedPublicationCost(t *testing.T) {
	for _, size := range []int{16, 1024, 8192} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "worker", "/root/worker", "wt")
			// Batch retained generations into one checkpoint so fixture setup does
			// not perform a growing checkpoint rewrite for every historical event.
			must(t, s.update(t.Context(), "worker", func(tree *Tree, member *Member) error {
				for i := range size {
					generation := uint64(i + 1)
					turn := fmt.Sprintf("wt-%d", i)
					tree.Effects = append(tree.Effects,
						Effect{ID: fmt.Sprintf("interrupt-%d", i), Caller: "root", CallerTurn: "rt", CallerGeneration: 1, Target: "worker", Kind: "interrupt", State: "confirmed", Generation: generation, Turn: turn},
						Effect{ID: fmt.Sprintf("worker:%d:%s:final", generation, turn), Caller: "worker", CallerTurn: turn, CallerGeneration: generation, Target: "root", Kind: "final", State: "pending", Outcome: Interrupted},
					)
					member.Turn = turn
				}
				member.Generation, member.Status, member.HostRunning = uint64(size), Interrupted, false
				return nil
			}))
			publication := s.Interruptions("root", nil)
			lastID := fmt.Sprintf("interrupt-%d", size-1)
			if publication.Outcome("root", "interrupt-0") != Interrupted || publication.Outcome("root", lastID) != Interrupted {
				t.Fatal("retained history was not indexed")
			}
			var next *Interruptions
			allocations := testing.AllocsPerRun(100, func() {
				next = s.Interruptions("root", publication)
				if next != publication {
					t.Fatal("unchanged tree rebuilt its publication")
				}
			})
			if allocations != 0 {
				t.Fatalf("unchanged publication allocated with %d retained generations: %g", size, allocations)
			}
		})
	}
}

func TestQueuedOldInterruptCannotCancelNewGeneration(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	caller := identity("root", "rt")
	caller.Call = "old-interrupt"
	interrupt, err := s.PrepareAction(t.Context(), caller, "worker", "interrupt", "")
	must(t, err)
	must(t, s.HostCompleted(t.Context(), "worker", "wt", Completed, HostFailure{}))
	caller.Call = "new-followup"
	next, err := s.PrepareAction(t.Context(), caller, "worker", "followup", "new work")
	must(t, err)
	_, err = s.ClaimEffect(t.Context(), "root", interrupt.ID)
	must(t, err)
	m, _ := s.Member("worker")
	tree, err := s.Snapshot("root")
	must(t, err)
	if m.Status != Pending || tree.Effect("root", next.ID).State == "canceled" {
		t.Fatalf("old interrupt generation=%d next generation=%d target=%s next effect=%s displayed outcome=%s", interrupt.Generation, next.Generation, m.Status, tree.Effect("root", next.ID).State, s.Interruptions("root", nil).Outcome("root", interrupt.ID))
	}
	if got := s.Interruptions("root", nil).Outcome("root", interrupt.ID); got != Completed {
		t.Fatalf("stale interruption lost its captured work's outcome: %s", got)
	}
}
