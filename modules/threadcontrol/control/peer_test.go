package control

import (
	"testing"
	"time"
)

func TestForeignMessageWakesBothModesThroughCompaction(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, compact := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{false: "/plain", true: "/compact"}[compact], func(t *testing.T) {
				s, _ := fixture(t)
				caller, now := identity("root", "rt"), time.Now()
				child(t, s, "root", "child", "/root/child", "ct")
				remote := RemoteTarget{Session: "foreign-root", Thread: "foreign", Task: "/root/worker", Turn: "ft", Generation: 1, Status: Working}
				_, err := s.registerWait(t.Context(), caller, mode, nil, nil, now, compact, false, "")
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
				origin := Origin(remote)
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

func TestForeignMessageConflicts(t *testing.T) {
	s, _ := fixture(t)
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
