package control

import (
	"slices"
	"testing"
	"time"
)

func TestWaitYieldUsesRetainedWake(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		t.Run(string(mode), func(t *testing.T) {
			s, session := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			caller, now := identity("root", "rt"), time.Unix(100, 0)
			_, err := s.RegisterWait(t.Context(), caller, mode, nil, new(int64(300000)), now)
			must(t, err)
			first, _ := s.Member("root")
			_, err = s.RegisterWait(t.Context(), caller, mode, nil, new(int64(600000)), now.Add(time.Minute))
			must(t, err)
			repeated, _ := s.Member("root")
			if !repeated.Wait.YieldAt.Equal(*first.Wait.YieldAt) {
				t.Fatal("re-execution changed the retained yield deadline")
			}
			if due := s.Due("root", now.Add(time.Hour)); len(due) != 0 {
				t.Fatal("uncommitted stop armed yield")
			}
			stop(t, s, caller)
			if due := s.Due("root", now.Add(5*time.Minute-time.Nanosecond)); len(due) != 0 {
				t.Fatal("yield became due early")
			}
			if due := s.Due("root", now.Add(5*time.Minute)); !slices.Equal(due, []string{"root"}) {
				t.Fatal("yield did not use ordinary deadline scheduling")
			}
			wake, err := claimWake(t, s, t.Context(), "root", now.Add(5*time.Minute))
			must(t, err)
			if wake == nil || wake.State != "yielded" || wake.Event != nil || len(wake.Events) != 0 {
				t.Fatalf("yield fabricated an outcome: %+v", wake)
			}
			progress := WaitProgress{Agents: []AgentProgress{{Task: "/root/child", Changes: "mchanges amber1 --summary"}}}
			must(t, s.RetainWaitProgress(t.Context(), "root", caller.Call, "context", progress, map[string]int{"capture": 2}))
			must(t, s.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Uncertain: true, Error: "lost acknowledgement"}))
			if use, _, _ := s.FeatureUse("root"); use.ProgressWaits != 1 || use.ProgressYields != 0 {
				t.Fatal("uncertain yield counted", use)
			}
			if len(s.YieldCursor("root", "context")) != 0 {
				t.Fatal("uncertain delivery consumed capture cursor")
			}
			root, _ := s.Member("root")
			restored := New()
			must(t, restored.AdmitRoot(t.Context(), session, root))
			retained, _ := restored.Member("root")
			if retained.Wait.Progress == nil || retained.Wait.Progress.Agents[0].Changes != progress.Agents[0].Changes || len(restored.Due("root", now.Add(time.Hour))) != 0 {
				t.Fatal("restore lost frozen yield or rearmed it")
			}
			must(t, restored.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Reconciled: true, Turn: "next"}))
			must(t, restored.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Reconciled: true, Turn: "next"}))
			if use, _, _ := restored.FeatureUse("root"); use.ProgressWaits != 0 || use.ProgressYields != 1 {
				t.Fatal("restored counts or duplicate confirmation", use)
			}
			if restored.YieldCursor("root", "context")["capture"] != 2 || restored.WaitPublication("root").State != "yielded" {
				t.Fatal("confirmed yield lost progress cursor or presentation reason")
			}
		})
	}
}

func TestWaitYieldPreservesEarlierOutcomesAndCompaction(t *testing.T) {
	for _, reason := range []string{"disabled", "timeout", "message", "completion", "compaction", "superseded", "execution failed"} {
		t.Run(reason, func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			caller, now := identity("root", "rt"), time.Unix(100, 0)
			yield := new(int64(300000))
			var timeout *int64
			if reason == "disabled" {
				yield = nil
			}
			if reason == "timeout" {
				timeout = new(int64(10))
			}
			_, err := s.registerWait(t.Context(), caller, WaitAny, timeout, yield, now, reason == "compaction", false, "")
			must(t, err)
			if reason == "execution failed" {
				must(t, s.Execution(t.Context(), "root", "rt", caller.Item, "", false))
			} else {
				stop(t, s, caller)
			}
			switch reason {
			case "message":
				message(t, s, identity("child", "ct"), "update")
			case "completion":
				must(t, s.HostCompleted(t.Context(), "child", "ct", Completed, HostFailure{}))
				deliver(t, s, "child")
			case "superseded":
				_, err = s.HumanInput(t.Context(), "root", "human")
				must(t, err)
			}
			wake, err := claimWake(t, s, t.Context(), "root", now.Add(5*time.Minute))
			must(t, err)
			if reason == "message" || reason == "completion" || reason == "timeout" {
				if wake == nil || wake.State == "yielded" {
					t.Fatalf("yield replaced %s: %+v", reason, wake)
				}
			} else if wake != nil {
				t.Fatalf("%s allowed yield: %+v", reason, wake)
			}
			if reason == "compaction" {
				compact, err := s.ClaimEffect(t.Context(), "root", caller.Call+":compact")
				must(t, err)
				must(t, s.EffectAcknowledged(t.Context(), "root", compact.ID, Receipt{Error: "reset failed"}))
				wake, err = claimWake(t, s, t.Context(), "root", now.Add(5*time.Minute))
				must(t, err)
				if wake == nil || wake.State != "yielded" || wake.Compaction.Error != "reset failed" {
					t.Fatalf("reset lost pending yield: %+v", wake)
				}
			}
		})
	}
}

func TestSkippedYieldRetainsWaitAuthority(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, reason := range []string{"unchanged", "completion", "message", "timeout"} {
			t.Run(string(mode)+"/"+reason, func(t *testing.T) {
				s, _ := fixture(t)
				child(t, s, "root", "child", "/root/child", "ct")
				caller, now := identity("root", "rt"), time.Unix(100, 0)
				_, err := s.RegisterWait(t.Context(), caller, mode, nil, new(int64(600000)), now)
				must(t, err)
				stop(t, s, caller)
				now = now.Add(10 * time.Minute)
				wake, err := claimWake(t, s, t.Context(), "root", now)
				must(t, err)
				if wake == nil || wake.State != "yielded" {
					t.Fatal("missing candidate yield")
				}
				var messageID string
				switch reason {
				case "completion":
					must(t, s.HostCompleted(t.Context(), "child", "ct", Completed, HostFailure{}))
				case "message":
					effect, err := s.PrepareAction(t.Context(), identity("child", "ct"), "/root", "message", "new message")
					must(t, err)
					messageID = effect.ID
				case "timeout":
					// Expiry may cross the capture acquisition boundary.
					must(t, s.update(t.Context(), "root", func(_ *Tree, member *Member) error {
						member.Wait.Deadline = new(now)
						return nil
					}))
				}
				must(t, s.SkipUnchangedYield(t.Context(), "root", caller.Call, now))
				member, _ := s.Member("root")
				if member.Wait.Yielded || member.HostRunning || member.Status != Waiting || member.Wait.Stop != "committed" || member.Wait.ID != caller.Call {
					t.Fatalf("empty progress lost the logical wait: %+v", member)
				}
				if reason == "unchanged" {
					if member.Wait.State != "registered" || !member.Wait.YieldAt.Equal(now.Add(10*time.Minute)) || len(s.Due("root", now)) != 0 || s.WaitPublication("root").Settled() {
						t.Fatalf("empty progress settled or immediately rearmed: %+v", member.Wait)
					}
					return
				}
				if reason == "completion" {
					deliver(t, s, "child")
				} else if reason == "message" {
					settle(t, s, "child", messageID, Receipt{})
				}
				if reason != "timeout" {
					_, err := s.PrepareWake(t.Context(), "root", now)
					must(t, err)
				}
				claimed, err := s.ClaimEffect(t.Context(), "root", caller.Call)
				must(t, err)
				if claimed == nil || claimed.Wake.State == "yielded" || reason == "timeout" && claimed.Wake.State != "timeout" || reason == "message" && claimed.Wake.Event.Kind != "message" {
					t.Fatalf("empty progress lost %s authority: %+v", reason, claimed)
				}
			})
		}
	}
}
