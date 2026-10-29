package control

import (
	"testing"
	"time"
)

// Exercise the host-event/state contract through the same Service entrypoints
// used by native bindings, including retained effects and dispatch admission.
func TestHostEventLogicalStateTable(t *testing.T) {
	cases := []struct {
		state, event string
		status       Status
		wait         string
		running      bool
		stop         bool
		final        int
		completion   int
		rejected     bool
	}{
		{"registered", "complete", Working, "registered", false, true, 0, 1, false},
		{"committed", "complete", Waiting, "registered", false, true, 0, 0, false},
		{"stopped", "complete", Waiting, "registered", false, true, 0, 0, false},
		{"registered", "fail", Failed, "canceled", false, false, 1, 0, false},
		{"committed", "fail", Failed, "canceled", false, true, 1, 0, false},
		{"committed", "recover", Waiting, "registered", false, true, 0, 0, false},
		{"registered", "interrupt", Interrupted, "canceled", false, false, 1, 0, false},
		{"committed", "interrupt", Interrupted, "canceled", false, true, 1, 0, false},
		{"registered", "compact", Working, "registered", true, false, 0, 0, false},
		{"committed", "compact", Waiting, "registered", false, true, 0, 0, false},
		{"stopped", "compact", Waiting, "registered", false, true, 0, 0, false},
		{"registered", "input", Working, "canceled", true, false, 0, 0, false},
		{"stopped", "input", Working, "canceled", false, false, 0, 0, false},
		{"queued", "input", Working, "canceled", false, false, 0, 0, false},
		{"dispatching", "input", Waiting, "dispatching", false, true, 0, 0, true},
		{"registered", "human_interrupt", Working, "canceled", true, false, 0, 0, false},
		{"stopped", "human_interrupt", Interrupted, "canceled", false, false, 1, 0, false},
		{"dispatching", "human_interrupt", Waiting, "dispatching", false, true, 0, 0, true},
		{"registered", "yield", Working, "registered", true, false, 0, 0, false},
		{"registered", "execution_success", Working, "registered", true, false, 0, 0, false},
		{"registered", "execution_failure", Working, "canceled", true, false, 0, 0, false},
		{"committed", "execution_failure", Working, "canceled", true, false, 0, 0, false},
		{"registered", "reexecute", Working, "registered", true, false, 0, 0, false},
		{"stopped", "reexecute", Waiting, "registered", false, true, 0, 0, true},
		{"queued", "reexecute", Waiting, "queued", false, true, 0, 0, true},
		{"registered", "other_execution", Working, "registered", true, false, 0, 0, true},
		{"stopped", "other_execution", Waiting, "registered", false, true, 0, 0, true},
		{"registered", "inject", Working, "registered", true, false, 0, 0, false},
		{"stopped", "inject", Waiting, "registered", false, true, 0, 0, false},
		{"stopped", "read_injection", Waiting, "registered", false, true, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.state+"/"+tc.event, func(t *testing.T) {
			s, _ := fixture(t)
			ctx, now := t.Context(), time.Unix(100, 0)
			child(t, s, "root", "worker", "/root/worker", "wt")
			child(t, s, "worker", "leaf", "/root/worker/leaf", "lt")
			id := identity("worker", "wt")
			timeout := int64(50)
			original, err := s.RegisterWait(ctx, id, nil, true, "", &timeout, now)
			must(t, err)
			must(t, s.AnswerCompleted(ctx, id.Thread, id.Turn, "answer", "retained answer"))
			if tc.state != "registered" {
				must(t, s.Execution(ctx, id.Thread, id.Turn, id.Item, "", true))
				local, err := s.ClaimLocalStop(ctx, id.Thread, id.Turn, id.Item)
				must(t, err)
				if local == nil {
					t.Fatal("successful execution did not commit its stop")
				}
			}
			if tc.state == "stopped" || tc.state == "queued" || tc.state == "dispatching" {
				must(t, s.HostCompleted(ctx, id.Thread, id.Turn, Completed, HostFailure{}))
			}
			if tc.state == "queued" || tc.state == "dispatching" {
				wake, err := s.PrepareWake(ctx, id.Thread, now.Add(50*time.Millisecond))
				must(t, err)
				if wake == nil {
					t.Fatal("deadline did not queue wake")
				}
				if tc.state == "dispatching" {
					claimed, err := s.ClaimEffect(ctx, id.Thread, wake.ID)
					must(t, err)
					if claimed == nil {
						t.Fatal("queued wake did not dispatch")
					}
				}
			}
			var injection string
			switch tc.event {
			case "complete", "fail", "interrupt", "recover":
				status := map[string]Status{"complete": Completed, "fail": Failed, "interrupt": Interrupted, "recover": Failed}[tc.event]
				err = s.HostCompleted(ctx, id.Thread, id.Turn, status, HostFailure{Message: "host failure", Recoverable: tc.event == "recover"})
			case "compact":
				err = s.CheckpointWait(ctx, id.Thread, id.Turn)
			case "input":
				_, err = s.HumanInput(ctx, id.Thread, "human-input")
			case "human_interrupt":
				turn, interruptErr := s.HumanInterrupt(ctx, id.Thread, original.ID)
				err = interruptErr
				if err == nil && tc.running && turn != id.Turn {
					t.Fatalf("running interruption lost host identity: %q", turn)
				}
			case "yield", "execution_success", "execution_failure":
				handle := ""
				if tc.event == "yield" {
					handle = "cell:running"
				}
				err = s.Execution(ctx, id.Thread, id.Turn, id.Item, handle, tc.event == "execution_success")
			case "reexecute", "other_execution":
				repeated := id
				repeated.Call = "new-call"
				if tc.event == "other_execution" {
					repeated.Item = "other-cell"
				}
				var result WaitResult
				result, err = s.RegisterWait(ctx, repeated, nil, true, "", &timeout, now.Add(time.Second))
				if err == nil && result.ID != original.ID {
					t.Fatalf("re-execution replaced wait: %+v", result)
				}
			case "inject", "read_injection":
				action, actionErr := s.PrepareAction(ctx, identity("leaf", "lt"), id.Thread, "message", "addressed input")
				must(t, actionErr)
				injection = action.Caller + ":" + action.ID
				_, err = s.ClaimEffect(ctx, action.Caller, action.ID)
				must(t, err)
				err = s.EffectAcknowledged(ctx, action.Caller, action.ID, Receipt{})
				if tc.event == "read_injection" {
					must(t, err)
					err = s.InputConsumed(ctx, id, []string{MessageItemID(injection)})
				}
			default:
				t.Fatal("unknown test event")
			}
			if (err != nil) != tc.rejected {
				t.Fatalf("transition error = %v, rejected = %v", err, tc.rejected)
			}
			state, err := s.Snapshot(id.Thread)
			must(t, err)
			member := state.Members[id.Thread]
			if member.Status != tc.status || member.Wait.State != tc.wait || member.HostRunning != tc.running || (member.Stop != nil) != tc.stop {
				t.Fatalf("logical disposition = %+v, wait = %+v; want %+v", member, member.Wait, tc)
			}
			if member.LastAnswer != "retained answer" || member.Generation != 1 || member.Wait.ID != original.ID && tc.event != "recover" {
				t.Fatalf("transition replaced answers/work/wait identity: %+v", member)
			}
			if tc.event != "recover" && !member.Wait.Deadline.Equal(now.Add(50*time.Millisecond)) {
				t.Fatal("reused wait deadline changed")
			}
			if len(effectsOf(state, "final")) != tc.final || len(effectsOf(state, "completion")) != tc.completion {
				t.Fatalf("incorrect terminal/completion publication: %+v", state.Effects)
			}
			if tc.event == "yield" || tc.event == "execution_success" {
				local, err := s.ClaimLocalStop(ctx, id.Thread, id.Turn, id.Item)
				must(t, err)
				if (local != nil) != (tc.event == "execution_success") {
					t.Fatalf("execution admitted incorrect stop: %+v", local)
				}
			}
			if injection != "" {
				wake, err := s.PrepareWake(ctx, id.Thread, now)
				must(t, err)
				if (wake != nil) != (tc.state == "stopped" && tc.event == "inject") {
					t.Fatalf("injection/read woke incorrectly: %+v", wake)
				}
			}
		})
	}
}
