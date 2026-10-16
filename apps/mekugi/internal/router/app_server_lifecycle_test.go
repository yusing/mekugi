package router

import "testing"

func TestAppServerLifecycleCompactAdmission(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			h := appServerLifecycle{thread: "main"}
			h.compaction.begin(true)
			if h.acceptsInput() || !h.starting() || !h.busy() {
				t.Fatal("starting compaction admitted input or lost its busy state")
			}
			if ackFirst {
				h.compactResponse(false)
				if h.acceptsInput() || !h.starting() {
					t.Fatal("acknowledgement completed a turn that has not started")
				}
			}
			h.started("compact")
			if h.acceptsInput() || h.starting() {
				t.Fatal("running compaction admitted input or still awaited a turn")
			}
			h.completed(success)
			if !ackFirst {
				if h.acceptsInput() || !h.busy() || h.compaction.takeContinuation() {
					t.Fatal("completion overtook the compact acknowledgement")
				}
				h.compactResponse(false)
			}
			if !h.acceptsInput() || h.busy() || h.compaction.takeContinuation() != success {
				t.Fatalf("settled compact: ackFirst=%v success=%v state=%+v", ackFirst, success, h)
			}
			h.completed(success)
			h.compactResponse(false)
			if h.compaction.takeContinuation() {
				t.Fatal("duplicate observations revived continuation")
			}
		}
	}
}

func TestAppServerLifecycleStartAdmission(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		h := appServerLifecycle{thread: "main"}
		h.submit(composerSubmission{composerDraft: composerDraft{text: "task"}, id: "input"})
		if ackFirst {
			h.acknowledge()
		} else {
			h.started("work")
		}
		if h.acceptsInput() {
			t.Fatal("one observation settled both submission and turn start")
		}
		if ackFirst {
			h.started("work")
		} else {
			h.acknowledge()
		}
		if !h.acceptsInput() || h.pendingStart.id != "input" {
			t.Fatal("acknowledged start lost steering admission or pending commit")
		}
		h.interruption.begin("work")
		if h.acceptsInput() {
			t.Fatal("interrupting turn accepted a steer")
		}
		h.completed(false)
		if !h.acceptsInput() || h.interruption.target != "" {
			t.Fatal("completion did not retire the interrupt target")
		}
	}
}

func TestAppServerLifecycleSettingsRestoration(t *testing.T) {
	h := appServerLifecycle{thread: "main"}
	h.settings.restoreEffort = true
	h.settings.begin(map[string]any{"collaborationMode": "host snapshot"}, "")
	h.settings.finish() // A failed RPC cannot discharge restoration intent.
	if h.acceptsInput() || h.settings.pending() {
		t.Fatal("failed settings operation lost the independent restoration gate")
	}
	h.settings.restoredEffort()
	if !h.acceptsInput() {
		t.Fatal("confirmed restoration did not release input")
	}
}

func TestAppServerLifecycleRejectionKeepsIssuedInterrupt(t *testing.T) {
	h := appServerLifecycle{thread: "main"}
	h.compaction.begin(true)
	h.interruption.deferUntilStart()
	h.started("compact")
	h.interruption.begin("compact")
	h.compactResponse(true)
	if h.interruption.beforeStart || h.acceptsInput() || h.interruption.target != "compact" {
		t.Fatal("late rejection forgot the host interrupt already in flight")
	}
	h.completed(false)
	if !h.acceptsInput() {
		t.Fatal("completion did not release the rejected operation")
	}
}

func TestAppServerLifecycleReplacementRetiresOperations(t *testing.T) {
	h := appServerLifecycle{thread: "old", turn: "old-turn", resumePendingEffort: true}
	h.compaction.begin(true)
	h.interruption.deferUntilStart()
	h.shellCommand.begin(composerDraft{text: "!pwd"}, "old-turn")
	h.settings.begin(map[string]any{"effort": "high"}, "old-turn")
	h.settings.restoreEffort = true
	h.replacement.resume("new")
	h.submit(composerSubmission{composerDraft: composerDraft{text: "old input"}})
	h.unsent = []composerDraft{{text: "new input"}}
	h.queued = []composerDraft{{text: "next input"}}
	h.leaveThread()
	if h.starting() || h.compaction.pending() || h.compaction.takeContinuation() ||
		h.interruption.beforeStart || h.shellCommand.blocksInput("") ||
		h.settings.blocksInput() || h.replacement.pending() || h.pendingStart.text != "" {
		t.Fatalf("replacement inherited a live operation: %+v", h)
	}
	if len(h.unsent) != 1 || len(h.queued) != 1 || !h.takeDefaultEffort() || h.takeDefaultEffort() {
		t.Fatal("replacement lost queued input or failed to transfer target intent once")
	}
}
