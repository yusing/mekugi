package control

import "testing"

func TestHumanChildInputAdmissionAndSettlement(t *testing.T) {
	for _, state := range []string{"idle", "running", "rejected", "uncertain", "committed"} {
		t.Run(state, func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "worker", "/root/worker", "original")
			if state != "running" {
				must(t, s.HostCompleted(t.Context(), "worker", "original", Completed, HostFailure{}))
			}
			before, _ := s.Member("worker")
			effect, err := s.HumanInput(t.Context(), "worker", "human")
			must(t, err)
			member, _ := s.Member("worker")
			if effect == nil || effect.Kind != "human" || effect.State != "dispatching" || effect.Target != "worker" || !member.Reserved() {
				t.Fatalf("missing child reservation: %+v, %+v", effect, member)
			}
			if state == "running" {
				if effect.Turn != "original" || member.Generation != before.Generation {
					t.Fatal("steer replaced running work")
				}
			} else if effect.Turn != "" || member.Generation != before.Generation+1 {
				t.Fatal("idle input did not reserve a new work generation")
			}
			caller := identity("root", "rt")
			if _, err := s.PrepareAction(t.Context(), caller, "worker", "followup", "competing assignment"); err == nil {
				t.Fatal("followup raced admitted human input")
			}
			if _, err := s.HumanInput(t.Context(), "worker", "competing-human"); err == nil {
				t.Fatal("concurrent human admission escaped the effect claim")
			}
			turn := "human-turn"
			if state == "running" {
				turn = "original"
			}
			receipt := Receipt{Turn: turn}
			if state == "rejected" || state == "uncertain" || state == "committed" {
				receipt = Receipt{Error: "host refusal", Uncertain: state != "rejected"}
			}
			must(t, s.EffectAcknowledged(t.Context(), "worker", "human", receipt))
			if state == "uncertain" || state == "committed" {
				must(t, s.HumanInputCommitted(t.Context(), "worker", turn, "unrelated"))
				tree, err := s.Snapshot("worker")
				must(t, err)
				if tree.Effect("worker", "human").State != "uncertain" {
					t.Fatal("unrelated input settled uncertainty")
				}
				if state == "committed" {
					must(t, s.RequestStarted(t.Context(), identity("worker", turn), false))
					must(t, s.HumanInputCommitted(t.Context(), "worker", turn, "human"))
				} else {
					member, _ = s.Member("worker")
					if !member.Reserved() {
						t.Fatal("uncertain input released its slot")
					}
					return
				}
			}
			if state == "rejected" {
				member, _ = s.Member("worker")
				if member.Reserved() || member.Status != Failed {
					t.Fatal("known refusal retained capacity")
				}
			} else {
				must(t, s.AnswerCompleted(t.Context(), "worker", turn, "human assignment result"))
				must(t, s.HostCompleted(t.Context(), "worker", turn, Completed, HostFailure{}))
			}
			must(t, s.EffectAcknowledged(t.Context(), "worker", "human", receipt))
			tree, err := s.Snapshot("worker")
			must(t, err)
			member = tree.Members["worker"]
			if member.HostRunning || member.Reserved() {
				t.Fatal("late acknowledgement revived completed work")
			}
			finals := 0
			for _, effect := range tree.Effects {
				if effect.Kind == "final" && effect.Caller == "worker" && effect.CallerGeneration == member.Generation {
					finals++
					if effect.Target != "root" || state != "rejected" && effect.Message != "human assignment result" {
						t.Fatalf("wrong result recipient or content: %+v", effect)
					}
				}
			}
			if finals != 1 {
				t.Fatalf("logical results = %d", finals)
			}
		})
	}
}

func TestHumanInputPreservesMainAndOrdering(t *testing.T) {
	s, _ := fixture(t)
	before, err := s.Snapshot("root")
	must(t, err)
	effect, err := s.HumanInput(t.Context(), "root", "ordinary-main")
	must(t, err)
	after, err := s.Snapshot("root")
	must(t, err)
	if effect != nil || len(after.Effects) != len(before.Effects) || after.Members["root"].Generation != before.Members["root"].Generation {
		t.Fatal("ordinary Main input acquired child coordination")
	}
	child(t, s, "root", "worker", "/root/worker", "original")
	must(t, s.HostCompleted(t.Context(), "worker", "original", Completed, HostFailure{}))
	caller := identity("root", "rt")
	_, err = s.PrepareAction(t.Context(), caller, "worker", "followup", "agent assignment")
	must(t, err)
	if _, err := s.HumanInput(t.Context(), "worker", "after-followup"); err == nil {
		t.Fatal("human input raced an earlier followup")
	}
}

func TestRejectedIdleFollowupReleasesReservation(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "original")
	must(t, s.HostCompleted(t.Context(), "worker", "original", Completed, HostFailure{}))
	caller := identity("root", "rt")
	effect, err := s.PrepareAction(t.Context(), caller, "worker", "followup", "new assignment")
	must(t, err)
	claimed, err := s.ClaimEffect(t.Context(), "root", effect.ID)
	must(t, err)
	if claimed == nil {
		t.Fatal("followup was not dispatched")
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", effect.ID, Receipt{Error: "known host refusal"}))
	member, _ := s.Member("worker")
	if member.Reserved() || member.Status != Failed {
		t.Fatalf("rejected followup leaked capacity: %+v", member)
	}
}
