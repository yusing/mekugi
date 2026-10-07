package router

import (
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

const compactContinuationText = "Continue the task from where you left off before compaction."

func queueBusyCompact(t *testing.T, u *appServerUI, w *appServerTestInput) appServerTurnRequest {
	t.Helper()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t")
	appServerTestTurnEnd(t, u, "work", "completed")
	return appServerOneRequest(t, w, "thread/compact/start", "")
}

func TestAppServerCompactQueuedContinuesOnceAfterAckAndTurn(t *testing.T) {
	for _, beforeAck := range []bool{false, true} {
		t.Run(fmt.Sprint("turnBeforeAck=", beforeAck), func(t *testing.T) {
			u, w := newAppServerTestUI()
			r := queueBusyCompact(t, u, w)
			ack := fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID)
			if !beforeAck {
				appServerTestMessage(t, u, ack)
				if w.Len() != 0 {
					t.Fatal("acknowledgement alone continued the task")
				}
			}
			appServerTestTurn(t, u, "compact-turn")
			appServerTestTurnEnd(t, u, "compact-turn", "completed")
			if beforeAck {
				if w.Len() != 0 {
					t.Fatal("task continued before compact acknowledgement")
				}
				appServerTestTurnEnd(t, u, "compact-turn", "completed")
				appServerTestMessage(t, u, ack)
			}
			start := appServerOneRequest(t, w, "turn/start", compactContinuationText)
			appServerTestTurnEnd(t, u, "compact-turn", "completed")
			appServerTestMessage(t, u, ack)
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"continued"}}}`, start.ID))
			appServerTestTurnEnd(t, u, "compact-turn", "completed")
			appServerTestTurnEnd(t, u, "continued", "completed")
			if w.Len() != 0 || u.compaction.continueTask || u.compaction.ackPending || u.compaction.running() {
				t.Fatalf("duplicate completion restarted task: wire=%q pending=%v", w.String(), u.compaction.continueTask)
			}
		})
	}
}

func TestAppServerCompactQueuedFollowupWins(t *testing.T) {
	for _, tab := range []bool{false, true} {
		t.Run(fmt.Sprint("tab=", tab), func(t *testing.T) {
			u, w := newAppServerTestUI()
			r := queueBusyCompact(t, u, w)
			key := "\r"
			if tab {
				key = "\t"
			}
			appServerTestKeys(t, u, "follow-up"+key)
			appServerTestTurn(t, u, "compact-turn")
			appServerTestTurnEnd(t, u, "compact-turn", "completed")
			if w.Len() != 0 {
				t.Fatal("follow-up overtook acknowledgement")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
			start := appServerOneRequest(t, w, "turn/start", "follow-up")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"follow-up-turn"}}}`, start.ID))
			appServerTestTurnEnd(t, u, "follow-up-turn", "completed")
			if w.Len() != 0 {
				t.Fatalf("automatic continuation followed user input: %s", w.String())
			}
		})
	}
}

func TestAppServerCompactQueuedConsecutiveCommandsContinueOnlyAfterLast(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t/compact\t")
	appServerTestTurnEnd(t, u, "work", "completed")
	for i := range 2 {
		r := appServerOneRequest(t, w, "thread/compact/start", "")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
		turn := fmt.Sprintf("compact-%d", i)
		appServerTestTurn(t, u, turn)
		appServerTestTurnEnd(t, u, turn, "completed")
	}
	appServerOneRequest(t, w, "turn/start", compactContinuationText)
}

func TestAppServerCompactQueuedFailedOrInterruptedDoesNotContinue(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		for _, beforeAck := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/turnBeforeAck=%v", status, beforeAck), func(t *testing.T) {
				u, w := newAppServerTestUI()
				r := queueBusyCompact(t, u, w)
				ack := fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID)
				if !beforeAck {
					appServerTestMessage(t, u, ack)
				}
				appServerTestTurn(t, u, "compact-turn")
				appServerTestTurnEnd(t, u, "compact-turn", status)
				if beforeAck {
					appServerTestMessage(t, u, ack)
				}
				if w.Len() != 0 || u.compaction.continueTask {
					t.Fatalf("%s compaction continued: %s", status, w.String())
				}
				appServerTestKeys(t, u, "/compact\r")
				retry := appServerOneRequest(t, w, "thread/compact/start", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, retry.ID))
				appServerTestTurn(t, u, "retry")
				appServerTestTurnEnd(t, u, "retry", "completed")
				if w.Len() != 0 {
					t.Fatalf("idle retry inherited continuation: %s", w.String())
				}
			})
		}
	}
}

func TestAppServerCompactQueuedRejectedAfterCompletionDoesNotContinue(t *testing.T) {
	u, w := newAppServerTestUI()
	r := queueBusyCompact(t, u, w)
	appServerTestTurn(t, u, "compact-turn")
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if w.Len() != 0 || u.draft != "/compact" || u.compaction.continueTask {
		t.Fatalf("RPC rejection continued or lost command: wire=%q draft=%q", w.String(), u.draft)
	}
	appServerTestKeys(t, u, "\r")
	retry := appServerOneRequest(t, w, "thread/compact/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, retry.ID))
	appServerTestTurn(t, u, "retry")
	appServerTestTurnEnd(t, u, "retry", "completed")
	if w.Len() != 0 {
		t.Fatalf("idle retry revived old continuation: %s", w.String())
	}
}

func TestAppServerCompactQueuedCancelBeforeRunPreservesMain(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprint("locked=", locked), func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.interruptLocked = locked
			appServerTestTurn(t, u, "work")
			appServerTestKeys(t, u, "/compact\t")
			appServerTestKeys(t, u, "after compact\r")
			appServerTestKeys(t, u, "next turn\t\x03")
			if w.Len() != 0 || u.turn != "work" || u.interruption.target != "" || u.interruption.beforeStart || u.draft != "/compact\nafter compact\nnext turn" || u.compaction.continueTask || len(u.unsent)+len(u.queued) != 0 {
				t.Fatalf("queued cancellation touched Main or lost input: wire=%q draft=%q turn=%q", w.String(), u.draft, u.turn)
			}
			appServerTestTurnEnd(t, u, "work", "completed")
			if w.Len() != 0 {
				t.Fatalf("cancelled compaction ran after Main ended: %s", w.String())
			}
		})
	}
}

func TestAppServerCompactQueuedCancelPreservesStartingMain(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "task\r")
	start := appServerOneRequest(t, w, "turn/start", "task")
	appServerTestKeys(t, u, "/compact\t\x03")
	if w.Len() != 0 || !u.starting() || u.interruption.beforeStart || u.submission.text != "task" || u.draft != "/compact" {
		t.Fatalf("compact cancellation cancelled the pending Main start: wire=%q draft=%q", w.String(), u.draft)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"work"}}}`, start.ID))
	appServerTestTurn(t, u, "work")
	if w.Len() != 0 || u.turn != "work" {
		t.Fatalf("compact cancellation interrupted newly started Main: %s", w.String())
	}
}

func TestAppServerCompactQueuedCancelPreservesInFlightSteer(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "steer\r")
	steer := appServerOneRequest(t, w, "turn/steer", "steer")
	appServerTestKeys(t, u, "waiting steer\r/compact\t\x03")
	if w.Len() != 0 || u.interruption.target != "" || u.submission.text != "steer" || u.draft != "waiting steer\n/compact" {
		t.Fatalf("compact cancellation touched the in-flight steer: wire=%q draft=%q", w.String(), u.draft)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"work"}}`, steer.ID))
	appServerTestUserMessage(t, u, "steer-item", steer.Params.ClientUserMessageID, "steer")
	appServerTestTurnEnd(t, u, "work", "completed")
	if w.Len() != 0 || u.draft != "waiting steer\n/compact" {
		t.Fatalf("cancelled input was resent: wire=%q draft=%q", w.String(), u.draft)
	}
}

func TestAppServerCompactCancelThenNormalMainInterrupt(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t\x03")
	if w.Len() != 0 {
		t.Fatal("compact cancellation interrupted Main")
	}
	appServerTestKeys(t, u, "\x03") // Clear the restored command first.
	if w.Len() != 0 || u.draft != "" {
		t.Fatal("clearing restored command interrupted Main")
	}
	appServerTestKeys(t, u, "\x03")
	appServerOneRequest(t, w, "turn/interrupt", "")
	if u.interruption.target != "work" {
		t.Fatal("normal Main interruption was not preserved")
	}
}

func TestAppServerCompactCancelCompletionBeforeAckDoesNotQuitOrContinue(t *testing.T) {
	u, w := newAppServerTestUI()
	r := queueBusyCompact(t, u, w)
	appServerTestTurn(t, u, "compact-turn")
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerTestKeys(t, u, "\x03")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	if w.Len() != 0 || u.compaction.continueTask || u.interruption.beforeStart {
		t.Fatalf("completion racing cancellation continued task: %s", w.String())
	}
	appServerTestKeys(t, u, "new task\r")
	appServerOneRequest(t, w, "turn/start", "new task")
	appServerTestTurn(t, u, "new-work")
	if w.Len() != 0 || u.interruption.target != "" {
		t.Fatal("old compaction cancellation interrupted the new Main turn")
	}
}

func TestUISnapshotNativeCompactQueuedCancel(t *testing.T) {
	u, w := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.model, u.reasoningEffort = "snapshot-model", "high"
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t")
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-queued", rows)
	appServerTestKeys(t, u, "\x03")
	rows, _ = u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-queued-cancel", rows)
	if w.Len() != 0 {
		t.Fatal("cancellation interrupted Main")
	}
}

func TestAppServerCompactQueuedInterruptBeforeCompactStartsCancelsContinuation(t *testing.T) {
	u, w := newAppServerTestUI()
	r := queueBusyCompact(t, u, w)
	appServerTestKeys(t, u, "\x03")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurn(t, u, "compact-turn")
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	// A host completion racing the interrupt must not revive continuation.
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	if w.Len() != 0 || u.compaction.continueTask {
		t.Fatalf("cancelled compaction continued: %s", w.String())
	}
}

func TestAppServerCompactQueuedInterruptDuringCompactionCancelsContinuation(t *testing.T) {
	u, w := newAppServerTestUI()
	r := queueBusyCompact(t, u, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurn(t, u, "compact-turn")
	appServerTestKeys(t, u, "\x03")
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	if w.Len() != 0 || u.compaction.continueTask {
		t.Fatalf("interrupt racing compaction completion restarted task: %s", w.String())
	}
}

func TestAppServerCompactCancelRestoresWaitingInputDespiteSuccessfulCompletion(t *testing.T) {
	for _, phase := range []string{"starting", "running", "completed-before-ack"} {
		t.Run(phase, func(t *testing.T) {
			u, w := newAppServerTestUI()
			r := queueBusyCompact(t, u, w)
			if phase == "running" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
			}
			if phase != "starting" {
				appServerTestTurn(t, u, "compact-turn")
			}
			if phase == "completed-before-ack" {
				appServerTestTurnEnd(t, u, "compact-turn", "completed")
			}
			appServerTestKeys(t, u, "waiting enter\rwaiting tab\t\x03")
			if u.draft != "waiting enter\nwaiting tab" || len(u.unsent)+len(u.queued) != 0 || u.compaction.continueTask {
				t.Fatalf("cancellation did not restore waiting input: draft=%q unsent=%+v queued=%+v", u.draft, u.unsent, u.queued)
			}
			if phase != "running" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
			}
			if phase == "starting" {
				appServerTestTurn(t, u, "compact-turn")
			}
			if phase != "completed-before-ack" {
				interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
				appServerTestTurnEnd(t, u, "compact-turn", "completed")
			}
			if w.Len() != 0 || u.draft != "waiting enter\nwaiting tab" {
				t.Fatalf("completion racing cancellation dispatched waiting input: wire=%q draft=%q", w.String(), u.draft)
			}
			appServerTestKeys(t, u, "\r")
			appServerOneRequest(t, w, "turn/start", "waiting enter\nwaiting tab")
			appServerTestTurn(t, u, "retry")
			if w.Len() != 0 || u.interruption.target != "" {
				t.Fatal("compaction cancellation interrupted the retried input")
			}
		})
	}
}

func TestUISnapshotNativeCompactQueuedContinuation(t *testing.T) {
	u, w := newAppServerTestUI()
	u.proxy = &mekugiProxy{journalCompaction: "auto"}
	u.session.start(u.thread, "")
	u.agents = newLiveActivityView()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.model, u.reasoningEffort = "snapshot-model", "high"
	r := queueBusyCompact(t, u, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurn(t, u, "compact-turn")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "compact-turn", "item": appServerItem{ID: "compaction", Type: "contextCompaction"}})
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerOneRequest(t, w, "turn/start", u.compactionText(compactContinuationText))
	for i := range u.view.entries {
		u.view.entries[i].Observed = u.now()
	}
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-queued-continuation", rows)
}

func TestAppServerCompactSteerWaitsForInterruptAndCompletion(t *testing.T) {
	for _, beforeAck := range []bool{false, true} {
		for _, status := range []string{"interrupted", "completed"} {
			t.Run(fmt.Sprintf("%s/completionBeforeAck=%v", status, beforeAck), func(t *testing.T) {
				u, w := newAppServerTestUI()
				appServerTestTurn(t, u, "work")
				appServerTestKeys(t, u, "/compact\r")
				interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
				ack := fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID)
				appServerTestKeys(t, u, "follow-up\rnext turn\t")
				if beforeAck {
					appServerTestTurnEnd(t, u, "work", status)
				} else {
					appServerTestMessage(t, u, ack)
				}
				if w.Len() != 0 {
					t.Fatal("compaction overtook interruption acknowledgement or turn completion")
				}
				appServerTestTurnEnd(t, u, "unrelated", "completed")
				if beforeAck {
					appServerTestMessage(t, u, ack)
				} else {
					appServerTestTurnEnd(t, u, "work", status)
				}
				compact := appServerOneRequest(t, w, "thread/compact/start", "")
				appServerTestMessage(t, u, ack)
				appServerTestTurnEnd(t, u, "work", status)
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, compact.ID))
				appServerTestTurn(t, u, "compact")
				appServerTestTurnEnd(t, u, "compact", "completed")
				appServerOneRequest(t, w, "turn/start", "follow-up")
				if u.draft != "" || len(u.queued) != 1 || u.queued[0].text != "next turn" {
					t.Fatalf("waiting input lost or restored: draft=%q queued=%+v", u.draft, u.queued)
				}
			})
		}
	}
}

func TestAppServerCompactSteerRestoresHostOwnedSteer(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "uncommitted\r")
	steer := appServerOneRequest(t, w, "turn/steer", "uncommitted")
	appServerTestKeys(t, u, "/compact\r")
	if w.Len() != 0 {
		t.Fatal("compact overtook unresolved input submission")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"work"}}`, steer.ID))
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestTurnEnd(t, u, "work", "interrupted")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	compact := appServerOneRequest(t, w, "thread/compact/start", "")
	if u.draft != "uncommitted" {
		t.Fatalf("host-owned steer not restored: %q", u.draft)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, compact.ID))
	appServerTestTurn(t, u, "compact")
	appServerTestTurnEnd(t, u, "compact", "completed")
	appServerOneRequest(t, w, "turn/start", compactContinuationText)
}

func TestAppServerCompactSteerWaitsForStartingTurn(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "task\r")
	start := appServerOneRequest(t, w, "turn/start", "task")
	appServerTestKeys(t, u, "/compact\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"work"}}}`, start.ID))
	if w.Len() != 0 {
		t.Fatal("interrupt dispatched before host turn identity")
	}
	appServerTestTurn(t, u, "work")
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "work", "interrupted")
	appServerOneRequest(t, w, "thread/compact/start", "")
	if u.draft != "task" {
		t.Fatalf("uncommitted starting input not restored: %q", u.draft)
	}
}

func TestAppServerCompactSteerInterruptRejected(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint("completed=", completed), func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestTurn(t, u, "work")
			appServerTestKeys(t, u, "/compact\rwaiting\r")
			interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
			if completed {
				appServerTestTurnEnd(t, u, "work", "completed")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, interrupt.ID))
			if w.Len() != 0 || u.draft != "/compact\nwaiting" || u.compaction.pending() || u.compaction.interrupting {
				t.Fatalf("rejected interruption compacted or lost input: wire=%q draft=%q", w.String(), u.draft)
			}
			appServerTestTurnEnd(t, u, "work", "completed")
			if w.Len() != 0 {
				t.Fatal("rejected command later ran")
			}
		})
	}
}

func TestAppServerCompactSteerCancelDuringInterrupt(t *testing.T) {
	for _, beforeAck := range []bool{false, true} {
		t.Run(fmt.Sprint("completionBeforeAck=", beforeAck), func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestTurn(t, u, "work")
			appServerTestKeys(t, u, "/compact\rwaiting\r\x03")
			interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
			if u.draft != "/compact\nwaiting" {
				t.Fatalf("cancel did not restore input: %q", u.draft)
			}
			ack := fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID)
			if beforeAck {
				appServerTestTurnEnd(t, u, "work", "interrupted")
				appServerTestMessage(t, u, ack)
			} else {
				appServerTestMessage(t, u, ack)
				appServerTestTurnEnd(t, u, "work", "interrupted")
			}
			if w.Len() != 0 || u.compaction.pending() || u.compaction.continueTask || u.compaction.interrupting {
				t.Fatalf("cancelled compaction dispatched: %s", w.String())
			}
		})
	}
}

func TestUISnapshotNativeCompactSteerInterrupt(t *testing.T) {
	u, w := newAppServerTestUI()
	u.proxy = &mekugiProxy{journalCompaction: "auto"}
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.model, u.reasoningEffort = "snapshot-model", "high"
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\r")
	appServerOneRequest(t, w, "turn/interrupt", "")
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-steer-interrupt", rows)
	appServerTestKeys(t, u, "\x03")
	rows, _ = u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-steer-cancel", rows)
}

func TestAppServerCompactSteerInterruptsWaitingQuestion(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "turn")
	questionTestSync(t, u, "question", false)
	u.hideQuestions()
	appServerTestKeys(t, u, "/compact\r")
	requests := appServerTurnRequests(t, w)
	found := false
	for _, request := range requests {
		if request.Method == "turn/interrupt" {
			found = true
		}
		if request.Method == "thread/compact/start" {
			t.Fatal("compaction overtook interruption")
		}
	}
	if !found {
		t.Fatalf("question blocked explicit compaction: %+v", requests)
	}
}

func TestAppServerCompactSteerEditCancelsIntent(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\r")
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestKeys(t, u, "\x1b[1;3A")
	appServerTestTurnEnd(t, u, "work", "interrupted")
	if !u.compaction.interruptAckPending || u.compaction.interrupting {
		t.Fatal("editing cleared acknowledgement gate or retained compact intent")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	if w.Len() != 0 || u.draft != "/compact" || u.compaction.pending() {
		t.Fatalf("edited command dispatched or lost: wire=%q draft=%q", w.String(), u.draft)
	}
}
