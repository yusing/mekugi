package control

import (
	"testing"
	"time"
)

func TestAttributedMessageAcknowledgementGatesWake(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	root := identity("root", "rt")
	_, err := s.RegisterWait(t.Context(), root, []string{"worker"}, false, nil, time.Now())
	must(t, err)
	stop(t, s, root)
	caller := identity("worker", "wt")
	caller.Call = "message-1"
	action, err := s.PrepareAction(t.Context(), caller, "/root", "message", "payload\nuntouched")
	must(t, err)
	claimed, err := s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if claimed == nil || claimed.Kind != "message" {
		t.Fatalf("message claim = %+v", claimed)
	}
	wake, err := claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	if wake != nil {
		t.Fatal("unconfirmed message woke receiver")
	}
	must(t, s.EffectAcknowledged(t.Context(), caller.Thread, action.ID, Receipt{}))
	must(t, s.EffectAcknowledged(t.Context(), caller.Thread, action.ID, Receipt{}))
	wake, err = claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil || wake.Event == nil || wake.Event.Source != "worker" || wake.Event.Message != "payload\nuntouched" {
		t.Fatalf("attributed wake = %+v", wake)
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	if len(deliveries(tree)) != 1 || tree.Members["root"].HostRunning {
		t.Fatal("message duplicated or started idle receiver")
	}
}

func TestFollowupWaitsForCommittedLocalStop(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	child(t, s, "worker", "leaf", "/root/worker/leaf", "lt")
	id := identity("worker", "wt")
	_, err := s.RegisterWait(t.Context(), id, []string{"leaf"}, false, nil, time.Now())
	must(t, err)
	must(t, s.Execution(t.Context(), id.Thread, id.Turn, id.Item, "", true))
	claimed, err := s.ClaimLocalStop(t.Context(), id.Thread, id.Turn, id.Item)
	must(t, err)
	if claimed == nil {
		t.Fatal("local stop was not committed")
	}
	caller := identity("root", "rt")
	action, err := s.PrepareAction(t.Context(), caller, "worker", "followup", "new instructions")
	must(t, err)
	dispatch, err := s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if dispatch != nil {
		t.Fatal("follow-up raced committed local stop")
	}
	must(t, s.HostCompleted(t.Context(), "worker", "wt", Completed, ""))
	dispatch, err = s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if dispatch == nil || dispatch.Kind != "followup" || dispatch.Generation != 1 {
		t.Fatalf("settled follow-up = %+v", dispatch)
	}
	hostStarted(t, s, "worker", "wt2")
	must(t, s.HostCompleted(t.Context(), "worker", "wt2", Completed, ""))
	must(t, s.EffectAcknowledged(t.Context(), caller.Thread, action.ID, Receipt{Turn: "wt2"}))
	tree, err := s.Snapshot("root")
	must(t, err)
	if tree.Members["worker"].HostRunning || len(effectsOf(tree, "final")) != 1 {
		t.Fatal("late acknowledgement revived completed work or local stop delivered a result")
	}
}

func TestInterruptWaitingTaskAndRestartAdmission(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	child(t, s, "worker", "leaf", "/root/worker/leaf", "lt")
	id := identity("worker", "wt")
	_, err := s.RegisterWait(t.Context(), id, nil, true, nil, time.Now())
	must(t, err)
	stop(t, s, id)
	caller := identity("root", "rt")
	action, err := s.PrepareAction(t.Context(), caller, "worker", "interrupt", "")
	must(t, err)
	dispatch, err := s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if dispatch != nil {
		t.Fatal("addressed an expired host turn")
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	if tree.Members["worker"].Status != Interrupted || tree.Members["worker"].Reserved() || len(effectsOf(tree, "final")) != 1 {
		t.Fatal("logical interrupt did not settle exactly one result")
	}
	caller.Call = "restart"
	action, err = s.PrepareAction(t.Context(), caller, "worker", "followup", "resume")
	must(t, err)
	if action.Generation != 2 {
		t.Fatalf("restart generation = %d", action.Generation)
	}
	if _, err := s.PrepareAction(t.Context(), caller, "/root", "interrupt", ""); err == nil {
		t.Fatal("agent acquired root control")
	}
}

func TestControlUncertaintyAndColdResume(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	caller := identity("root", "rt")
	action, err := s.PrepareAction(t.Context(), caller, "worker", "interrupt", "")
	must(t, err)
	dispatch, err := s.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if dispatch == nil || dispatch.Turn != "wt" {
		t.Fatalf("interrupt = %+v", dispatch)
	}
	root, err := s.Caller(caller)
	must(t, err)
	restored := New()
	must(t, restored.AdmitRoot(t.Context(), session, root))
	tree, err := restored.Snapshot("root")
	must(t, err)
	if tree.Effect(caller.Thread, action.ID).State != "uncertain" {
		t.Fatalf("lost acknowledgement = %s", tree.Effect(caller.Thread, action.ID).State)
	}
	dispatch, err = restored.ClaimEffect(t.Context(), caller.Thread, action.ID)
	must(t, err)
	if dispatch != nil {
		t.Fatal("cold resume repeated an uncertain host operation")
	}
}

func TestUncertainDeliveryPreservesArrivalOrder(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	root := identity("root", "rt")
	_, err := s.RegisterWait(t.Context(), root, nil, true, nil, time.Now())
	must(t, err)
	stop(t, s, root)
	worker := identity("worker", "wt")
	for _, id := range []string{"first", "second"} {
		worker.Call = id
		_, err := s.PrepareAction(t.Context(), worker, "root", "message", id)
		must(t, err)
	}
	_, err = s.ClaimEffect(t.Context(), "worker", "first")
	must(t, err)
	must(t, s.EffectAcknowledged(t.Context(), "worker", "first", Receipt{Uncertain: true}))
	if later, err := s.ClaimEffect(t.Context(), "worker", "second"); err != nil || later != nil {
		t.Fatal("delivery passed an uncertain predecessor", later, err)
	}
	must(t, s.EffectAcknowledged(t.Context(), "worker", "first", Receipt{Reconciled: true}))
	settle(t, s, "worker", "second", Receipt{})
	wake, err := claimWake(t, s, t.Context(), "root", time.Now())
	must(t, err)
	if wake == nil || wake.Event == nil || wake.Event.Message != "first" {
		t.Fatal("first delivered event was not selected", wake)
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", wake.ID, Receipt{Turn: "rt2"}))
	root.Turn, root.Call = "rt2", "next-wait"
	next, err := s.RegisterWait(t.Context(), root, nil, true, nil, time.Now())
	must(t, err)
	if next.Event == nil || next.Event.Message != "second" {
		t.Fatal("second event was lost or claimed out of order", next)
	}
}

func TestHumanInputSupersedesOnlyUnsentWake(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "dispatched"}[dispatched], func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "worker", "/root/worker", "wt")
			caller := identity("root", "rt")
			_, err := s.RegisterWait(t.Context(), caller, nil, true, nil, time.Now())
			must(t, err)
			stop(t, s, caller)
			message(t, s, identity("worker", "wt"), "ready")
			_, err = s.PrepareWake(t.Context(), "root", time.Now())
			must(t, err)
			if dispatched {
				_, err = s.ClaimEffect(t.Context(), "root", caller.Call)
				must(t, err)
			}
			_, err = s.HumanInput(t.Context(), "root", "human")
			if (err != nil) != dispatched {
				t.Fatal("human admission did not preserve dispatched ownership", err)
			}
			member, _ := s.Member("root")
			if !dispatched && (member.Wait.State != "canceled" || member.Stop != nil) {
				t.Fatal("human admission left stale local stop", member)
			}
		})
	}
}

func TestHumanInterruptUsesExactWaitAndSettlesOnce(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	caller := identity("root", "rt")
	_, err := s.RegisterWait(t.Context(), caller, nil, true, nil, time.Now())
	must(t, err)
	stop(t, s, caller)
	_, err = s.HumanInterrupt(t.Context(), "root", "stale")
	must(t, err)
	member, _ := s.Member("root")
	if member.Status != Waiting {
		t.Fatal("stale interruption settled current wait")
	}
	for range 2 {
		turn, err := s.HumanInterrupt(t.Context(), "root", caller.Call)
		must(t, err)
		if turn != "" {
			t.Fatal("logical interruption addressed an expired host turn")
		}
	}
	member, _ = s.Member("root")
	if member.Status != Interrupted || member.Reserved() || member.Wait.State != "canceled" {
		t.Fatal("logical interruption did not settle", member)
	}
}
