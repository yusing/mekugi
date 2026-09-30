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
	appServerTestKeys(t, u, "/compact\r")
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
			if w.Len() != 0 || u.continueAfterCompact || u.compactRequest || u.manualCompact {
				t.Fatalf("duplicate completion restarted task: wire=%q pending=%v", w.String(), u.continueAfterCompact)
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
	appServerTestKeys(t, u, "/compact\r/compact\r")
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
				if w.Len() != 0 || u.continueAfterCompact {
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
	if w.Len() != 0 || u.draft != "/compact" || u.continueAfterCompact {
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

func TestAppServerCompactQueuedInterruptBeforeRunRestoresCommand(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\r\x03")
	r := appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurnEnd(t, u, "work", "interrupted")
	if w.Len() != 0 || u.draft != "/compact" || u.continueAfterCompact {
		t.Fatalf("cancelled queue restarted: wire=%q draft=%q", w.String(), u.draft)
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
	if w.Len() != 0 || u.continueAfterCompact {
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
	if w.Len() != 0 || u.continueAfterCompact {
		t.Fatalf("interrupt racing compaction completion restarted task: %s", w.String())
	}
}

func TestUISnapshotNativeCompactQueuedContinuation(t *testing.T) {
	u, w := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.model, u.reasoningEffort = "snapshot-model", "high"
	r := queueBusyCompact(t, u, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, r.ID))
	appServerTestTurn(t, u, "compact-turn")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "compact-turn", "item": appServerItem{ID: "compaction", Type: "contextCompaction"}})
	appServerTestTurnEnd(t, u, "compact-turn", "completed")
	appServerOneRequest(t, w, "turn/start", compactContinuationText)
	for i := range u.view.entries {
		u.view.entries[i].Observed = u.now()
	}
	rows, _ := u.mainFrame(80, 16, 0)
	assertNativeUISnapshot(t, "native-compact-queued-continuation", rows)
}
