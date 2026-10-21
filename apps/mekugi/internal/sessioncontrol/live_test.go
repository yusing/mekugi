package sessioncontrol

import (
	"testing"
)

func TestAppServerLifecycleCompactAdmission(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		for _, success := range []bool{false, true} {
			h := Live{Thread: "main"}
			h.Compaction.Begin(CompactIntent{Continue: true})
			if h.AcceptsInput() || !h.Starting() || !h.Busy(false) {
				t.Fatal("starting compaction admitted input or lost its busy state")
			}
			if ackFirst {
				h.CompactResponse(false)
				if h.AcceptsInput() || !h.Starting() {
					t.Fatal("acknowledgement completed a turn that has not started")
				}
			}
			h.Started("compact")
			if h.AcceptsInput() || h.Starting() {
				t.Fatal("running compaction admitted input or still awaited a turn")
			}
			h.Completed(success)
			if !ackFirst {
				if h.AcceptsInput() || !h.Busy(false) || h.Compaction.Continue(false) {
					t.Fatal("completion overtook the compact acknowledgement")
				}
				h.CompactResponse(false)
			}
			if !h.AcceptsInput() || h.Busy(false) || h.Compaction.Continue(false) != success {
				t.Fatalf("settled compact: ackFirst=%v success=%v state=%+v", ackFirst, success, h)
			}
			h.Completed(success)
			h.CompactResponse(false)
			if h.Compaction.Continue(false) {
				t.Fatal("duplicate observations revived continuation")
			}
		}
	}
}

func TestAppServerLifecycleStartAdmission(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		h := Live{Thread: "main"}
		h.Submit("input", "")
		if ackFirst {
			h.AcknowledgeInput(false, false, false)
		} else {
			h.Started("work")
		}
		if h.AcceptsInput() {
			t.Fatal("one observation settled both submission and turn start")
		}
		if ackFirst {
			h.Started("work")
		} else {
			h.AcknowledgeInput(false, false, false)
		}
		if !h.AcceptsInput() || h.PendingStart() != "input" {
			t.Fatal("acknowledged start lost steering admission or pending commit")
		}
		h.Interrupt()
		if h.AcceptsInput() {
			t.Fatal("interrupting turn accepted a steer")
		}
		h.Completed(false)
		if !h.AcceptsInput() || h.Interruption.Target != "" {
			t.Fatal("completion did not retire the interrupt target")
		}
	}
}

func TestAppServerLifecycleSettingsRestoration(t *testing.T) {
	h := Live{Thread: "main"}
	h.Settings.RestoreEffort = true
	h.Settings.Admit(SettingsPlan{Action: SettingsThread, Change: map[string]any{"collaborationMode": "host snapshot"}})
	h.Settings.Finish() // A failed RPC cannot discharge restoration intent.
	if h.AcceptsInput() || h.Settings.Pending() {
		t.Fatal("failed settings operation lost the independent restoration gate")
	}
	h.Settings.RestoredEffort()
	if !h.AcceptsInput() {
		t.Fatal("confirmed restoration did not release input")
	}
}

func TestAppServerLifecycleRejectionKeepsIssuedInterrupt(t *testing.T) {
	h := Live{Thread: "main"}
	h.Compaction.Begin(CompactIntent{Continue: true})
	h.Interrupt()
	h.Started("compact")
	h.Interrupt()
	h.CompactResponse(true)
	if h.Interruption.BeforeStart || h.AcceptsInput() || h.Interruption.Target != "compact" {
		t.Fatal("late rejection forgot the host interrupt already in flight")
	}
	h.Completed(false)
	if !h.AcceptsInput() {
		t.Fatal("completion did not release the rejected operation")
	}
}

func TestAppServerLifecycleReplacementRetiresOperations(t *testing.T) {
	h := Live{Thread: "old", Turn: "old-turn", ResumeEffort: true}
	h.Compaction.Begin(CompactIntent{Continue: true})
	h.Interrupt()
	h.Shell.Begin("old-turn")
	h.Settings.Admit(SettingsPlan{Action: SettingsThread, Change: map[string]any{"effort": "high"}, Turn: "old-turn"})
	h.Settings.RestoreEffort = true
	h.Replacement.Resume("new")
	h.Submit("old-input", "")
	h.LeaveThread()
	if h.Starting() || h.Compaction.Pending() || h.Compaction.Continue(false) ||
		h.Interruption.BeforeStart || h.Shell.BlocksInput("") ||
		h.Settings.BlocksInput() || h.Replacement.Pending() || h.PendingStart() != "" {
		t.Fatalf("replacement inherited a live operation: %+v", h)
	}
	if !h.TakeResumeEffort() || h.TakeResumeEffort() {
		t.Fatal("replacement failed to transfer target intent once")
	}
}
