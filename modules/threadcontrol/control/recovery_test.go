package control

import (
	"testing"
	"time"
)

func TestRecoveryRequiresFreshCapturedDescendantOutcome(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "coordinator", "/root/coordinator", "ct")
	child(t, s, "coordinator", "leaf", "/root/coordinator/leaf", "lt")
	must(t, s.HostCompleted(t.Context(), "coordinator", "ct", Failed, HostFailure{Message: "invalid request"}))
	deliver(t, s, "coordinator")
	must(t, s.InputConsumed(t.Context(), identity("root", "rt"), []string{MessageItemID("coordinator:1:ct:final")}))
	must(t, s.HostCompleted(t.Context(), "root", "rt", Failed, HostFailure{Message: "overloaded", Recoverable: true}))
	if wake, err := s.PrepareWake(t.Context(), "root", time.Now()); err != nil || wake != nil {
		t.Fatalf("already-stopped coordinator woke recovery: %+v %v", wake, err)
	}
	must(t, s.HostCompleted(t.Context(), "leaf", "lt", Completed, HostFailure{}))
	deliver(t, s, "leaf")
	wake, err := s.PrepareWake(t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil {
		t.Fatal("fresh nested result did not wake through a terminal coordinator")
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	effect := tree.Effect("leaf", "leaf:1:lt:final")
	if effect.Consumed || effect.ClaimedBy != "" || tree.Members["root"].Wait.Terminal.Task != "/root/coordinator/leaf" {
		t.Fatal("recovery consumed another recipient's notification or lost the source")
	}
}

func TestPendingRecoveryRestoresWithoutRearmingDispatchedWake(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "child", "/root/child", "ct")
	caller := identity("root", "rt")
	_, err := s.RegisterWait(t.Context(), caller, nil, true, "", nil, time.Now())
	must(t, err)
	stop(t, s, caller)
	message(t, s, identity("child", "ct"), "progress")
	wake, err := claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	hostStarted(t, s, "root", "next")
	must(t, s.HostCompleted(t.Context(), "root", "next", Failed, HostFailure{Recoverable: true}))
	must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Uncertain: true, Error: "lost acknowledgement"}))
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	member, _ := restored.Member("root")
	if member.Status != Paused || member.Wait.ID != wake.ID || member.Wait.State != "uncertain" || member.RecoveryWait == nil || member.RecoveryWait.State != "paused" {
		t.Fatalf("restore lost dispatch or recovery identity: %+v", member)
	}
	must(t, restored.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Reconciled: true}))
	member, _ = restored.Member("root")
	if member.RecoveryWait != nil || member.Status != Paused || member.Wait.State != "paused" {
		t.Fatalf("confirmation rearmed paused recovery: %+v", member)
	}
	if wake, err := restored.PrepareWake(t.Context(), "root", time.Now()); err != nil || wake != nil {
		t.Fatalf("cold recovery dispatched without explicit work: %+v %v", wake, err)
	}
}

func TestRecoveryRetainsUnreadStoppedSiblingResult(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "done", "/root/done", "dt")
	child(t, s, "root", "busy", "/root/busy", "bt")
	must(t, s.HostCompleted(t.Context(), "done", "dt", Completed, HostFailure{}))
	deliver(t, s, "done")
	// This result arrived while root's provider request was in flight. No request
	// has consumed the attributed result yet.
	must(t, s.HostCompleted(t.Context(), "root", "rt", Failed, HostFailure{Recoverable: true}))
	wake, err := s.PrepareWake(t.Context(), "root", time.Now())
	must(t, err)
	root, _ := s.Member("root")
	tree, err := s.Snapshot("root")
	must(t, err)
	result := tree.Effect("done", "done:1:dt:final")
	t.Logf("root recovery targets=%v, unread result state=%s consumed=%t claim=%q; wake=%+v", root.Wait.Targets, result.State, result.Consumed, result.ClaimedBy, wake)
	if wake == nil {
		t.Fatal("unread direct-child result was excluded solely because its source is stopped")
	}
}
