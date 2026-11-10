package control

import (
	"strings"
	"testing"
	"time"
)

func summaryFixture(t *testing.T, mode WaitMode) (*Service, Identity, time.Time) {
	t.Helper()
	s, _ := fixture(t)
	child(t, s, "root", "first", "/root/first", "ft")
	child(t, s, "root", "second", "/root/second", "st")
	caller, now := identity("root", "rt"), time.Unix(100, 0)
	_, err := s.RegisterWait(t.Context(), caller, mode, nil, new(int64(300000)), now)
	must(t, err)
	stop(t, s, caller)
	_, err = claimWake(t, s, t.Context(), "root", now.Add(5*time.Minute))
	must(t, err)
	progress := WaitProgress{Agents: []AgentProgress{{Task: "/root/first", Changes: "amber1"}, {Task: "/root/second", Changes: "blue2 blue4"}}}
	must(t, s.BeginWaitSummaries(t.Context(), "root", caller.Call, "context", progress, map[string]int{"first-record": 2, "second-record": 1}))
	progress.Agents[0].Changes = "later-edits"
	return s, caller, now.Add(5 * time.Minute)
}

func summarySlot(t *testing.T, s *Service, index int) Effect {
	t.Helper()
	member, _ := s.Member("root")
	slot := member.Wait.Summaries[index]
	effect, err := s.ClaimEffect(t.Context(), "root", slot.ID)
	must(t, err)
	if effect == nil {
		t.Fatal("summary was not claimable")
	}
	return *effect
}

func replySummary(t *testing.T, s *Service, slot Effect, summary string) bool {
	t.Helper()
	_, accepted, err := s.ProgressReply(t.Context(), identity(slot.Target, slot.Turn), "agent:main", ProgressReplyPrefix+slot.ID+"\n"+summary)
	must(t, err)
	return accepted
}

func TestProgressSummariesFreezeCollectAndAcknowledgeTogether(t *testing.T) {
	s, caller, now := summaryFixture(t, WaitAny)
	first, second := summarySlot(t, s, 0), summarySlot(t, s, 1)
	for _, required := range []string{"amber1", "Progress checkpoint, not a new assignment", "completed tool results only", "longer replies are valid", "send_message", ProgressReplyPrefix + first.ID} {
		if !strings.Contains(first.Message, required) {
			t.Fatalf("request missing %q: %s", required, first.Message)
		}
	}
	if strings.Contains(first.Message, "later-edits") {
		t.Fatal("frozen selection mutated")
	}
	long := strings.Repeat("Observed validation is missing. ", 1000)
	if !replySummary(t, s, second, long) {
		t.Fatal("reply rejected before native acknowledgement")
	}
	must(t, s.AcknowledgeSummary(t.Context(), "root", second.ID, Receipt{Turn: second.Turn}))
	if wake, err := s.PrepareWake(t.Context(), "root", now); err != nil || wake != nil {
		t.Fatalf("one reply woke parent: %v %v", wake, err)
	}
	must(t, s.AcknowledgeSummary(t.Context(), "root", first.ID, Receipt{Turn: first.Turn}))
	if !replySummary(t, s, first, "Changed behavior to preserve the original assignment; validation not run.") {
		t.Fatal("first reply rejected")
	}
	if replySummary(t, s, first, "duplicate") {
		t.Fatal("duplicate was accepted")
	}
	if len(s.YieldCursor("root", "context")) != 0 {
		t.Fatal("summary collection consumed captures")
	}
	wake, err := claimWake(t, s, t.Context(), "root", now)
	must(t, err)
	if wake == nil || wake.State != "yielded" || wake.Progress.Agents[1].Summary != long || wake.Progress.Agents[0].Changes != "amber1" {
		t.Fatalf("combined yield lost frozen content: %+v", wake)
	}
	for _, thread := range []string{"first", "second"} {
		member, _ := s.Member(thread)
		if member.Generation != 1 || !member.HostRunning || member.Status != Working || member.LastAnswer != "" {
			t.Fatalf("summary changed assignment: %+v", member)
		}
	}
	must(t, s.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Turn: "continued"}))
	if s.YieldCursor("root", "context")["first-record"] != 2 || len(s.YieldCursor("root", "reset")) != 0 {
		t.Fatal("confirmed delivery lost recipient context")
	}
}

func TestProgressSummarySupersessionAndUnavailable(t *testing.T) {
	for _, scenario := range []string{"message", "deadline", "cancel", "completion", "failed", "stopped", "uncertain", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, now := summaryFixture(t, WaitAll)
			first, second := summarySlot(t, s, 0), summarySlot(t, s, 1)
			if !replySummary(t, s, second, "Second changes; tests not run.") {
				t.Fatal("reply rejected")
			}
			switch scenario {
			case "message":
				message(t, s, identity("first", "ft"), "ordinary message")
			case "deadline":
				must(t, s.update(t.Context(), "root", func(_ *Tree, member *Member) error { member.Wait.Deadline = &now; return nil }))
			case "cancel":
				_, err := s.HumanInput(t.Context(), "root", "human")
				must(t, err)
			case "completion":
				must(t, s.HostCompleted(t.Context(), "first", "ft", Completed, HostFailure{}))
				deliver(t, s, "first")
				must(t, s.HostCompleted(t.Context(), "second", "st", Completed, HostFailure{}))
				deliver(t, s, "second")
			case "failed":
				must(t, s.HostCompleted(t.Context(), "first", "ft", Failed, HostFailure{Message: "provider disconnected"}))
				deliver(t, s, "first")
			case "stopped":
				must(t, s.HostCompleted(t.Context(), "first", "ft", Interrupted, HostFailure{Message: "user interrupted"}))
				deliver(t, s, "first")
			case "uncertain":
				must(t, s.AcknowledgeSummary(t.Context(), "root", first.ID, Receipt{Uncertain: true, Error: "lost acknowledgement"}))
			case "generation":
				must(t, s.update(t.Context(), "first", func(_ *Tree, member *Member) error { member.Generation++; return nil }))
			}
			wake, err := claimWake(t, s, t.Context(), "root", now)
			must(t, err)
			if scenario == "failed" || scenario == "stopped" || scenario == "uncertain" || scenario == "generation" {
				if wake == nil || wake.State != "yielded" || !strings.HasPrefix(wake.Progress.Agents[0].Summary, "Summary unavailable:") {
					t.Fatalf("unresolved slot: %+v", wake)
				}
				if scenario == "failed" && !strings.Contains(wake.Progress.Agents[0].Summary, "provider disconnected") {
					t.Fatal("lost actual failure reason")
				}
			} else if scenario == "cancel" {
				if wake != nil {
					t.Fatal("cancellation yielded")
				}
			} else if wake == nil || wake.State == "yielded" || wake.Progress != nil {
				t.Fatalf("higher-priority event lost: %+v", wake)
			}
			_, accepted, _ := s.ProgressReply(t.Context(), identity(first.Target, first.Turn), "agent:main", ProgressReplyPrefix+first.ID+"\nlate reply")
			if accepted {
				t.Fatal("late reply revived collection")
			}
			must(t, s.AcknowledgeSummary(t.Context(), "root", first.ID, Receipt{Turn: "ft"}))
			if len(s.YieldCursor("root", "context")) != 0 {
				t.Fatal("abandoned/unconfirmed output consumed captures")
			}
		})
	}
}

func TestProgressSummaryReplyRequiresExactAdmittedWork(t *testing.T) {
	s, _, _ := summaryFixture(t, WaitAny)
	first := summarySlot(t, s, 0)
	for _, caller := range []Identity{identity("second", "st"), identity("first", "old-turn"), {Thread: "first", Session: "wrong-session", Turn: "ft"}} {
		_, accepted, _ := s.ProgressReply(t.Context(), caller, "agent:main", ProgressReplyPrefix+first.ID+"\nWrong work.")
		if accepted {
			t.Fatal("reply from unrelated work resolved the slot")
		}
	}
	_, accepted, err := s.ProgressReply(t.Context(), identity("first", "ft"), "agent:main/second", ProgressReplyPrefix+first.ID+"\nWrong recipient.")
	must(t, err)
	if accepted || !replySummary(t, s, first, "Exact admitted work; validation missing.") {
		t.Fatal("rejected replies changed the retained slot")
	}
}

func TestProgressSummaryRecoveryDoesNotAcknowledgeOmittedEvidence(t *testing.T) {
	s, caller, now := summaryFixture(t, WaitAny)
	for i := range 2 {
		slot := summarySlot(t, s, i)
		if !replySummary(t, s, slot, "Changes; validation missing.") {
			t.Fatal("reply rejected")
		}
	}
	_, err := claimWake(t, s, t.Context(), "root", now)
	must(t, err)
	must(t, s.RetainProgressOutput(t.Context(), "root", caller.Call, WaitProgress{Recovery: "mread amber", More: true}))
	must(t, s.EffectAcknowledged(t.Context(), "root", caller.Call, Receipt{Turn: "continued"}))
	if len(s.YieldCursor("root", "context")) != 0 {
		t.Fatal("recovery pointer consumed omitted capture evidence")
	}
}

func TestProgressSummaryNativeToolBoundaryIsOnceAndNonterminal(t *testing.T) {
	s, _, _ := summaryFixture(t, WaitAny)
	first := summarySlot(t, s, 0)
	must(t, s.AcknowledgeSummary(t.Context(), "root", first.ID, Receipt{Turn: "ft"}))
	text, err := s.ToolBoundarySummaries(t.Context(), identity("first", "ft"))
	must(t, err)
	if text != first.Message {
		t.Fatal("native boundary lost checkpoint request")
	}
	again, err := s.ToolBoundarySummaries(t.Context(), identity("first", "ft"))
	must(t, err)
	if again != "" || !replySummary(t, s, first, "Native checkpoint, validation missing.") {
		t.Fatal("checkpoint repeated or reply rejected")
	}
}
