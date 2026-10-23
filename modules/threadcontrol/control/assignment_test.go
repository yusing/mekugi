package control

import "testing"

func TestColdFollowupCompletesOnlyUnfinishedAdmission(t *testing.T) {
	for _, phase := range []string{"assignment_input", "admit", "initial"} {
		t.Run(phase, func(t *testing.T) {
			s, session := fixture(t)
			caller := identity("root", "rt")
			caller.Call = "spawn"
			spawn, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "new", Message: "Original assignment.", Settings: Settings{Workspace: t.TempDir()}})
			must(t, err)
			settle(t, s, "root", spawn.ID, Receipt{Thread: "new", Session: "new-session"})
			for current := "assignment_input"; current != phase; {
				settle(t, s, "root", spawn.ID, Receipt{})
				if current == "assignment_input" {
					current = "admit"
				} else {
					current = "initial"
				}
			}
			restored := New()
			must(t, restored.Restore(t.Context(), session))
			caller.Call, caller.Turn = "followup", "resumed"
			must(t, restored.RequestStarted(t.Context(), caller, true))
			_, err = restored.PrepareAction(t.Context(), caller, "new", "followup", "New assignment.")
			must(t, err)
			for {
				action, err := restored.ClaimEffect(t.Context(), "root", spawn.ID)
				must(t, err)
				if action == nil {
					break
				}
				if action.Phase != "assignment_input" && action.Phase != "admit" {
					t.Fatalf("resumed superseded execution: %+v", action)
				}
				must(t, restored.EffectAcknowledged(t.Context(), "root", spawn.ID, Receipt{Phase: action.Phase}))
			}
			action, err := restored.ClaimEffect(t.Context(), "root", caller.Call)
			must(t, err)
			if action == nil || action.Message != "New assignment." {
				t.Fatalf("new assignment stranded: %+v", action)
			}
		})
	}
}

func TestInitialAssignmentSerializesFollowupAndInterrupt(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "followup", true: "interrupt"}[interrupt], func(t *testing.T) {
			s, _ := fixture(t)
			ctx := t.Context()
			caller := identity("root", "rt")
			caller.Call = "spawn"
			spawn, err := s.ReserveSpawn(ctx, caller, SpawnRequest{Name: "new", Message: "Work.", Settings: Settings{Workspace: t.TempDir()}, Limits: Limits{Concurrency: 8}})
			must(t, err)
			settle(t, s, "root", spawn.ID, Receipt{Thread: "new", Session: "new-session"})
			settle(t, s, "root", spawn.ID, Receipt{})
			settle(t, s, "root", spawn.ID, Receipt{})
			caller.Call = "followup"
			_, err = s.PrepareAction(ctx, caller, "new", "followup", "More work.")
			must(t, err)
			action, err := s.ClaimEffect(ctx, "root", caller.Call)
			must(t, err)
			if action != nil {
				t.Fatal("followup raced pending initial assignment")
			}
			if interrupt {
				caller.Call = "interrupt"
				_, err = s.PrepareAction(ctx, caller, "new", "interrupt", "")
				must(t, err)
				action, err = s.ClaimEffect(ctx, "root", caller.Call)
				must(t, err)
				if action != nil {
					t.Fatal("idle cancellation dispatched a host effect")
				}
				assignment, err := s.ClaimEffect(ctx, "root", spawn.ID)
				must(t, err)
				if assignment != nil {
					t.Fatal("canceled assignment was claimable")
				}
				action, err = s.ClaimEffect(ctx, "root", "followup")
				must(t, err)
				if action != nil {
					t.Fatal("canceled queued followup was claimable")
				}
				return
			}
			assignment, err := s.ClaimEffect(ctx, "root", spawn.ID)
			must(t, err)
			if assignment == nil || assignment.Phase != "initial" {
				t.Fatal("initial assignment was not claimable")
			}
			action, err = s.ClaimEffect(ctx, "root", "followup")
			must(t, err)
			if action != nil {
				t.Fatal("followup raced dispatched initial assignment")
			}
			must(t, s.EffectAcknowledged(ctx, "root", spawn.ID, Receipt{Turn: "nt", Phase: "initial"}))
			action, err = s.ClaimEffect(ctx, "root", "followup")
			must(t, err)
			if action == nil || action.Turn != "nt" {
				t.Fatal("confirmed initial assignment blocked queued followup")
			}
		})
	}
}

func TestCreationPhasesRetainOneReservationAndPauseOnRecovery(t *testing.T) {
	s, session := fixture(t)
	caller := identity("root", "rt")
	spawn, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "new", Message: "Work.", Settings: Settings{Workspace: t.TempDir()}})
	must(t, err)
	settle(t, s, "root", spawn.ID, Receipt{Thread: "new", Session: "new-session"})
	tree, err := s.Snapshot("root")
	must(t, err)
	if len(tree.Effects) != 1 || activeCount(&tree) != 2 || tree.Effects[0].Phase != "assignment_input" || tree.Members["new"].AudienceAdmitted {
		t.Fatal("creation did not retain exactly one reservation before admission", tree)
	}
	_, err = s.ClaimEffect(t.Context(), "root", spawn.ID)
	must(t, err)
	if err := s.EffectAcknowledged(t.Context(), "root", spawn.ID, Receipt{Thread: "new", Session: "new-session", Phase: "create"}); err == nil {
		t.Fatal("late creation acknowledgement advanced assignment retention")
	}
	restored := New()
	must(t, restored.AdmitRoot(t.Context(), session, tree.Members["root"]))
	if effect, err := restored.ClaimEffect(t.Context(), "root", spawn.ID); err != nil || effect != nil {
		t.Fatal("cold recovery repeated unacknowledged assignment", effect, err)
	}
	must(t, restored.EffectAcknowledged(t.Context(), "root", spawn.ID, Receipt{Reconciled: true, Phase: "assignment_input"}))
	tree, err = restored.Snapshot("root")
	must(t, err)
	if effect := tree.Effect("root", spawn.ID); effect.Phase != "admit" || effect.State != "paused" || tree.Members["new"].Status != Paused {
		t.Fatal("retained assignment reconciliation restarted paused creation", tree)
	}
}

func TestInterruptBeforeAdmissionSuppressesInitialAssignment(t *testing.T) {
	s, _ := fixture(t)
	caller := identity("root", "rt")
	spawn, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "new", Message: "Work.", Settings: Settings{Workspace: t.TempDir()}})
	must(t, err)
	settle(t, s, "root", spawn.ID, Receipt{Thread: "new", Session: "new-session"})
	caller.Call = "interrupt"
	_, err = s.PrepareAction(t.Context(), caller, "new", "interrupt", "")
	must(t, err)
	if effect, err := s.ClaimEffect(t.Context(), "root", caller.Call); err != nil || effect != nil {
		t.Fatal("interrupt bypassed pending audience admission", effect, err)
	}
	settle(t, s, "root", spawn.ID, Receipt{})
	settle(t, s, "root", spawn.ID, Receipt{})
	if effect, err := s.ClaimEffect(t.Context(), "root", caller.Call); err != nil || effect != nil {
		t.Fatal("idle logical interrupt dispatched a host effect", effect, err)
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	if tree.Effect("root", spawn.ID).State != "canceled" || tree.Members["new"].Status != Interrupted || !tree.Members["new"].AudienceAdmitted || tree.Members["new"].Reserved() {
		t.Fatal("interruption lost audience admission or ran initial work", tree)
	}
}
