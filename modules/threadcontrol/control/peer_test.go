package control

import (
	"slices"
	"testing"
	"time"
)

func TestForeignWaitMixedSelectionAndPublication(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		t.Run(string(mode), func(t *testing.T) {
			s, _ := fixture(t)
			child(t, s, "root", "child", "/root/child", "ct")
			caller, now := identity("root", "rt"), time.Now()
			remote := RemoteTarget{Session: "foreign-root", Thread: "foreign", Task: "/root/worker", Turn: "ft", Generation: 1, Status: Working, HostRunning: true}
			result, err := s.RegisterForeignWait(t.Context(), caller, []string{"child", "foreign"}, mode, nil, now, false, map[string]RemoteTarget{"foreign": remote}, "")
			must(t, err)
			if result.State != "waiting" || len(result.Targets) != 2 || result.Targets[1] != "foreign" {
				t.Fatalf("registration: %+v", result)
			}
			// Re-executing the enclosing call preserves its original subscription.
			again, err := s.RegisterForeignWait(t.Context(), caller, []string{"foreign"}, mode, nil, now, false, map[string]RemoteTarget{"foreign": remote}, "")
			must(t, err)
			if len(again.Targets) != 2 {
				t.Fatal("re-execution replaced selection")
			}
			before, err := s.Snapshot("root")
			must(t, err)
			changed, err := s.Changes("root")
			must(t, err)
			must(t, s.RefreshForeignWait(t.Context(), "root", caller.Call, remote))
			select {
			case <-changed:
				t.Fatal("unchanged foreign status published")
			default:
			}
			stop(t, s, caller)
			remote.Status, remote.HostRunning = Completed, false
			must(t, s.RefreshForeignWait(t.Context(), "root", caller.Call, remote))
			if before.Members["root"].Wait.Foreign["foreign"].Status != Working {
				t.Fatal("published snapshot mutated")
			}
			if mode == WaitAll {
				wake, err := claimWake(t, s, t.Context(), "root", now)
				must(t, err)
				if wake != nil {
					t.Fatal("one terminal woke mixed all-wait")
				}
				must(t, s.HostCompleted(t.Context(), "child", "ct", Completed, HostFailure{}))
				deliver(t, s, "child")
			}
			wake, err := claimWake(t, s, t.Context(), "root", now)
			must(t, err)
			if wake == nil {
				t.Fatal("foreign terminal did not wake")
			}
			if mode == WaitAny && (wake.Event == nil || wake.Event.Session != "foreign-root") || mode == WaitAll && (len(wake.Events) != 2 || !slices.ContainsFunc(wake.Events, func(event Event) bool { return event.Session == "foreign-root" })) {
				t.Fatalf("outcomes: %+v", wake)
			}
			if _, ok := s.Member("foreign"); ok {
				t.Fatal("foreign target admitted as local member")
			}
		})
	}
}

func TestForeignMessageWakesBothModesThroughCompaction(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, compact := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{false: "/plain", true: "/compact"}[compact], func(t *testing.T) {
				s, _ := fixture(t)
				caller, now := identity("root", "rt"), time.Now()
				remote := RemoteTarget{Session: "foreign-root", Thread: "foreign", Task: "/root/worker", Turn: "ft", Generation: 1, Status: Working, HostRunning: true}
				_, err := s.RegisterForeignWait(t.Context(), caller, []string{"foreign"}, mode, nil, now, compact, map[string]RemoteTarget{"foreign": remote}, "")
				must(t, err)
				stop(t, s, caller)
				if compact {
					effect, err := s.ClaimEffect(t.Context(), "root", caller.Call+":compact")
					must(t, err)
					if effect == nil {
						t.Fatal("missing compact effect")
					}
					must(t, s.EffectAcknowledged(t.Context(), "root", effect.ID, Receipt{}))
				}
				origin := Origin{Session: remote.Session, Thread: remote.Thread, Task: remote.Task, Turn: remote.Turn, Generation: remote.Generation, Status: remote.Status}
				effect, err := s.ReceiveMessage(t.Context(), origin, "root", "message", "progress")
				must(t, err)
				action, err := s.ClaimEffect(t.Context(), "root", effect.ID)
				must(t, err)
				if action == nil || action.Origin == nil || *action.Origin != origin {
					t.Fatalf("action: %+v", action)
				}
				must(t, s.EffectAcknowledged(t.Context(), "root", effect.ID, Receipt{}))
				wake, err := claimWake(t, s, t.Context(), "root", now)
				must(t, err)
				if wake == nil || wake.Event == nil || wake.Event.Source != "foreign" || wake.Event.Session != "foreign-root" || wake.Event.Message != "progress" {
					t.Fatalf("message wake: %+v", wake)
				}
				if compact && wake.Compaction == nil {
					t.Fatal("compaction outcome lost")
				}
			})
		}
	}
}

func TestForeignWaitSupersessionAndMessageConflicts(t *testing.T) {
	s, _ := fixture(t)
	caller := identity("root", "rt")
	remote := RemoteTarget{Session: "foreign-root", Thread: "foreign", Status: Working, HostRunning: true}
	_, err := s.RegisterForeignWait(t.Context(), caller, []string{"foreign"}, WaitAny, nil, time.Now(), false, map[string]RemoteTarget{"foreign": remote}, "")
	must(t, err)
	_, err = s.HumanInput(t.Context(), "root", "new-input")
	must(t, err)
	if s.ForeignWait("root", caller.Call) != nil {
		t.Fatal("superseded wait retains observers")
	}
	remote.Status, remote.HostRunning = Completed, false
	must(t, s.RefreshForeignWait(t.Context(), "root", caller.Call, remote))
	if wake, err := s.PrepareWake(t.Context(), "root", time.Now()); err != nil || wake != nil {
		t.Fatalf("superseded wait woke: %v %v", wake, err)
	}
	origin := Origin{Session: "foreign-root", Thread: "foreign", Task: "/root", Generation: 1}
	effect, err := s.ReceiveMessage(t.Context(), origin, "root", "call", "original")
	must(t, err)
	origin.Status = Waiting
	_, err = s.ReceiveMessage(t.Context(), origin, "root", "call", "original")
	must(t, err)
	_, err = s.ReceiveMessage(t.Context(), origin, "root", "call", "replacement")
	if err == nil {
		t.Fatal("conflicting repeat accepted")
	}
	retained, err := s.Effect("root", effect.ID)
	must(t, err)
	if retained.Message != "original" {
		t.Fatal("rejection changed retained message")
	}
}
