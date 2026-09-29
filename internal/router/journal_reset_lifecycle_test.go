package router

import (
	"encoding/json/jsontext"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

const resetLifecycleActiveGoal = `{"goal":{"objective":"task","status":"active","createdAt":1,"updatedAt":2}}`
const resetLifecyclePausedGoal = `{"goal":{"objective":"task","status":"paused","createdAt":1,"updatedAt":3}}`

func resetLifecyclePaused(t *testing.T, mode string) (*journalResetDriver, *appServerTestInput) {
	t.Helper()
	d, wire := resetDriverFixture(t, mode)
	resetDriverReply(t, d, resetLifecycleActiveGoal)
	resetDriverReply(t, d, resetLifecyclePausedGoal)
	return d, wire
}

func resetLifecycleRequireRetainedPause(t *testing.T, d *journalResetDriver) {
	t.Helper()
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.PausedGoal == nil || !samePausedGoal(intent.PausedGoal, d.paused) {
		t.Fatalf("paused goal evidence lost: intent=%+v err=%v", intent, err)
	}
	if d.active() || d.notice == "" {
		t.Fatalf("uncertain goal outcome must stop with a notice: active=%v notice=%q", d.active(), d.notice)
	}
}

func TestJournalResetDriverMalformedGoalRepliesStopWithoutRetry(t *testing.T) {
	for _, phase := range []string{"goal", "pause", "resume-check", "resume"} {
		t.Run(phase, func(t *testing.T) {
			d, wire := resetDriverFixture(t, "slice")
			if phase != "goal" {
				resetDriverReply(t, d, resetLifecycleActiveGoal)
			}
			if phase == "resume-check" || phase == "resume" {
				resetDriverReply(t, d, resetLifecyclePausedGoal)
				if err := d.cancel(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "resume" {
				resetDriverReply(t, d, resetLifecyclePausedGoal)
			}
			before := len(resetDriverMethods(t, wire))
			resetDriverReply(t, d, `{"goal":`)
			if d.active() || d.notice == "" {
				t.Fatalf("malformed %s reply left driver active or silent: %+v", phase, d)
			}
			if err := d.tick(time.Unix(200, 0)); err != nil {
				t.Fatal(err)
			}
			if got := len(resetDriverMethods(t, wire)); got != before {
				t.Fatalf("malformed reply sent more RPCs: %v", resetDriverMethods(t, wire))
			}
			intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "goal" {
				if intent != nil {
					t.Fatalf("initial goal decode failure retained reset intent: %+v", intent)
				}
			} else if phase == "pause" {
				if intent == nil || intent.Phase != "pausing" {
					t.Fatalf("uncertain pause lost durable dispatch evidence: %+v", intent)
				}
			} else {
				resetLifecycleRequireRetainedPause(t, d)
			}
		})
	}
}

func TestJournalResetDriverGoalRestoreRPCFailurePreservesPause(t *testing.T) {
	for _, phase := range []string{"resume-check", "resume"} {
		t.Run(phase, func(t *testing.T) {
			d, wire := resetLifecyclePaused(t, "slice")
			if err := d.cancel(); err != nil {
				t.Fatal(err)
			}
			if phase == "resume" {
				resetDriverReply(t, d, resetLifecyclePausedGoal)
			}
			before := len(resetDriverMethods(t, wire))
			handled, err := d.message(appserver.Message{ID: jsontext.Value(d.requestID), Error: &appserver.Error{Code: -1, Message: "unavailable"}}, time.Unix(103, 0))
			if err != nil || !handled {
				t.Fatalf("restore failure: handled=%v err=%v", handled, err)
			}
			resetLifecycleRequireRetainedPause(t, d)
			if err := d.tick(time.Unix(200, 0)); err != nil {
				t.Fatal(err)
			}
			if got := len(resetDriverMethods(t, wire)); got != before {
				t.Fatalf("restore failure retried: %v", resetDriverMethods(t, wire))
			}
			restarted := &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: d.client, workspace: d.workspace, thread: d.thread}
			if err := restarted.restore(); err != nil {
				t.Fatal(err)
			}
			if got := len(resetDriverMethods(t, wire)); got != before || restarted.notice == "" {
				t.Fatalf("restart must report, not replay, uncertain restore: RPCs=%v notice=%q", resetDriverMethods(t, wire), restarted.notice)
			}
		})
	}
}

func TestJournalResetDriverCancelDuringPauseWaitsForRestoration(t *testing.T) {
	d, wire := resetDriverFixture(t, "slice")
	resetDriverReply(t, d, resetLifecycleActiveGoal)
	if err := d.cancel(); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set")
	resetDriverReply(t, d, resetLifecyclePausedGoal)
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set", "thread/goal/get")
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.PausedGoal == nil {
		t.Fatalf("cancelled in-flight pause must retain confirmed goal until restoration: %+v %v", intent, err)
	}
	resetDriverReply(t, d, resetLifecyclePausedGoal)
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set", "thread/goal/get", "thread/goal/set")
	resetDriverReply(t, d, resetLifecycleActiveGoal)
	intent, err = d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent != nil || d.active() {
		t.Fatalf("cancel restoration did not finish: intent=%+v active=%v err=%v", intent, d.active(), err)
	}
}

func TestJournalResetDriverFastContinuationDefersNextSliceUntilGoalRestored(t *testing.T) {
	d, wire := resetLifecyclePaused(t, "off")
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Third")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverReply(t, d, `{"turn":{"id":"continued"}}`)
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"continued"}}`)
	if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "continued"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.completed("continued"); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set", "turn/start", "thread/goal/get")
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Turn != "first-turn" {
		t.Fatalf("next slice displaced outstanding restoration: %+v %v", intent, err)
	}
	resetDriverReply(t, d, resetLifecyclePausedGoal)
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set", "turn/start", "thread/goal/get", "thread/goal/set")
	resetDriverReply(t, d, resetLifecycleActiveGoal)
	resetDriverRequireMethods(t, wire, "thread/goal/get", "thread/goal/set", "turn/start", "thread/goal/get", "thread/goal/set", "thread/goal/get")
	intent, err = d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Turn != "continued" || intent.Path != "/3" {
		t.Fatalf("deferred next slice not processed: %+v %v", intent, err)
	}
}
