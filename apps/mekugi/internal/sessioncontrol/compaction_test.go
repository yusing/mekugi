package sessioncontrol

import "testing"

func TestCompactionPrerequisiteNeedsBothObservations(t *testing.T) {
	for _, acknowledgementFirst := range []bool{false, true} {
		var operation Compaction
		intent := CompactIntent{Continue: true}
		if operation.InterruptBefore(CompactIntent{WaitForIdle: true}, "main-turn") {
			t.Fatal("queued compaction interrupted Main")
		}
		if !operation.InterruptBefore(intent, "main-turn") || !operation.Pending() {
			t.Fatal("explicit compaction did not retain its prerequisite")
		}
		operation.Completed("unrelated-turn", true)
		if acknowledgementFirst {
			operation.Acknowledge(false)
		} else {
			operation.Completed("main-turn", false)
		}
		if !operation.Pending() || !operation.PreservesWaitingInput() {
			t.Fatal("one observation discharged both prerequisite gates")
		}
		if acknowledgementFirst {
			operation.Completed("main-turn", false)
		} else {
			operation.Acknowledge(false)
		}
		if operation.Pending() || !operation.PreservesWaitingInput() {
			t.Fatal("settled prerequisite blocked compaction or lost waiting input")
		}
		operation.Begin(intent)
		operation.Started("compact-turn")
		operation.Completed("main-turn", true)
		operation.Acknowledge(false)
		if !operation.Pending() || operation.Continue(false) {
			t.Fatal("old prerequisite completion finished compaction")
		}
		operation.Completed("compact-turn", true)
		if operation.Pending() || !operation.Continue(false) || operation.Continue(false) {
			t.Fatal("successful compaction did not continue exactly once")
		}
	}
}

func TestCompactionCancellationOwnsOnlyItsTurn(t *testing.T) {
	var operation Compaction
	operation.Begin(CompactIntent{Continue: true})
	if !operation.Cancel() || operation.InterruptTarget() != "" {
		t.Fatal("cancelled start invented a host turn identity")
	}
	operation.Started("compact-turn")
	if operation.InterruptTarget() != "compact-turn" {
		t.Fatal("cancelled start lost its eventual interrupt target")
	}
	operation.Completed("compact-turn", true)
	operation.Started("later-turn")
	operation.Acknowledge(false)
	if operation.InterruptTarget() != "" || operation.Pending() || operation.Continue(false) {
		t.Fatal("completed cancellation affected a later turn or revived continuation")
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
