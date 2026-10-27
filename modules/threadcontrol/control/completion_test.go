package control

import "testing"

func TestCompletionGuardTransitiveAndStaleHints(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	child(t, s, "a", "b", "/root/a/b", "bt")
	for _, thread := range []string{"a", "root"} {
		member, _ := s.Member(thread)
		must(t, s.AnswerCompleted(t.Context(), thread, member.Turn, "premature final"))
		must(t, s.HostCompleted(t.Context(), thread, member.Turn, Completed, HostFailure{}))
		must(t, s.HostCompleted(t.Context(), thread, member.Turn, Completed, HostFailure{}))
	}
	tree, err := s.Snapshot("root")
	must(t, err)
	if len(effectsOf(tree, "completion")) != 2 || len(effectsOf(tree, "final")) != 0 || tree.Members["root"].Status != Working || tree.Members["a"].Status != Working {
		t.Fatal("normal completion escaped active transitive descendants")
	}
	must(t, s.HostCompleted(t.Context(), "b", "bt", Completed, HostFailure{}))
	// Descendants settled before dispatch. Keep the original result and skip an
	// unnecessary model continuation for both ancestors.
	for _, thread := range []string{"a", "root"} {
		member, _ := s.Member(thread)
		effect, err := s.ClaimEffect(t.Context(), thread, member.Stop.Wait)
		must(t, err)
		member, _ = s.Member(thread)
		if effect != nil || member.Status != Completed {
			t.Fatalf("stale completion hint: %+v %+v", effect, member)
		}
	}
}

func TestCompletionGuardClaimFreezesTransitiveNames(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "a", "/root/a", "at")
	child(t, s, "a", "b", "/root/a/b", "bt")
	must(t, s.HostCompleted(t.Context(), "root", "rt", Completed, HostFailure{}))
	member, _ := s.Member("root")
	effect, err := s.ClaimEffect(t.Context(), "root", member.Stop.Wait)
	must(t, err)
	if effect == nil || effect.Message != "/root/a, /root/a/b" {
		t.Fatalf("claimed guard omitted transitive names: %+v", effect)
	}
}

func TestCompletionGuardAdmissionAndCancellation(t *testing.T) {
	for _, status := range []Status{Pending, Working, Waiting, Paused} {
		t.Run(string(status), func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			must(t, s.update(t.Context(), "child", func(_ *Tree, m *Member) error { m.Status = status; return nil }))
			must(t, s.HostCompleted(t.Context(), "root", "rt", Completed, HostFailure{}))
			member, _ := s.Member("root")
			if status == Paused {
				if member.Status != Completed {
					t.Fatal("paused descendant blocked completion")
				}
				return
			}
			if member.Status != Working || member.Stop == nil {
				t.Fatal("active reservation did not guard completion")
			}
			turn, err := s.HumanInterrupt(t.Context(), "root", member.Stop.Wait)
			must(t, err)
			member, _ = s.Member("root")
			if turn != "" || member.Status != Interrupted {
				t.Fatal("completion guard blocked explicit interruption")
			}
			tree, err := s.Snapshot("root")
			must(t, err)
			if effectsOf(tree, "completion")[0].State != "canceled" {
				t.Fatal("interruption retained a pending hint")
			}
		})
	}
	for _, status := range []Status{Interrupted, Failed} {
		t.Run(string(status), func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			must(t, s.HostCompleted(t.Context(), "root", "rt", status, HostFailure{}))
			tree, err := s.Snapshot("root")
			must(t, err)
			if tree.Members["root"].Status != status || len(effectsOf(tree, "completion")) != 0 {
				t.Fatal("guard replaced explicit host outcome")
			}
		})
	}
}
