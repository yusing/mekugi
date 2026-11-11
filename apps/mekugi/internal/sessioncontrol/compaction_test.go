package sessioncontrol

import "testing"

func TestCompactionCancellationOwnsOnlyItsRequest(t *testing.T) {
	for _, started := range []bool{false, true} {
		live := Live{Thread: "main", Turn: "original"}
		live.Compaction.Begin(CompactIntent{Continue: true})
		id := live.Compaction.NativeRequestID()
		if started {
			live.Started("compact-turn")
		}
		intent := live.Interrupt()
		if intent.ResetRequest != id || intent.Turn != "" || !intent.RestoreWaiting {
			t.Fatalf("manual cancellation interrupted unrelated host work: %+v", intent)
		}
		if again := live.Interrupt(); again.ResetRequest != "" || again.Turn != "" {
			t.Fatalf("duplicate cancellation: %+v", again)
		}
		live.Compaction.ObserveReset(ContextReset{RequestID: id, Generation: 1, Status: "accepted"})
		live.Compaction.ObserveReset(ContextReset{RequestID: id, Generation: 1, Status: "canceled"})
		live.Compaction.Acknowledge(false)
		if live.Compaction.Pending() || live.Compaction.Continue(false) {
			t.Fatal("canceled request retained reset or continuation")
		}
	}
}

func TestCompactionWaitingInputConsumesContinuation(t *testing.T) {
	var operation Compaction
	operation.Begin(CompactIntent{Continue: true})
	operation.Started("compact-turn")
	operation.Completed("compact-turn", true)
	if operation.Continue(true) || !operation.ContinuationPending() {
		t.Fatal("waiting input consumed continuation before acknowledgement")
	}
	operation.Acknowledge(false)
	if operation.Continue(true) || operation.ContinuationPending() || operation.Continue(false) {
		t.Fatal("automatic continuation competed with waiting user input")
	}
}
