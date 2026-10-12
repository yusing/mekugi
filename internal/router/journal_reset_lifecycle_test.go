package router

import (
	"fmt"
	"testing"
)

func TestJournalResetDriverFastContinuationDefersNextSliceUntilAcknowledged(t *testing.T) {
	d, wire := resetDriverFixture(t, "off")
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Third")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.start(); err != nil {
		t.Fatal(err)
	}
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
	resetDriverRequireMethods(t, wire, "turn/start")
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Turn != "first-turn" {
		t.Fatalf("next slice displaced outstanding acknowledgement: %+v %v", intent, err)
	}
	resetDriverReply(t, d, `{"turn":{"id":"continued"}}`)
	resetDriverRequireMethods(t, wire, "turn/start", "turn/start")
	intent, err = d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Turn != "continued" || intent.Path != "/3" {
		t.Fatalf("deferred next slice not processed: %+v %v", intent, err)
	}
}

func TestJournalContinuationDispatchesImmediately(t *testing.T) {
	d, wire := autoResumeFixture(t)
	if err := d.completed("answer"); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
}

func TestNativeJournalContinuationStopBeforeHostStart(t *testing.T) {
	for _, mode := range []string{"off", "slice"} {
		for _, outcome := range []string{"started", "rejected"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				d, wire := resetDriverFixture(t, mode)
				if err := d.start(); err != nil {
					t.Fatal(err)
				}
				requestID := d.requestID
				u, _ := newAppServerTestUI()
				u.ctx, u.proxy, u.client, u.thread, u.reset = t.Context(), d.proxy, d.client, d.thread, d
				u.ensureShell()
				if err := u.shell.send("\x03"); err != nil {
					t.Fatal(err)
				}
				if u.quitRequested || !u.interruption.beforeStart {
					t.Fatal("pending continuation did not defer interruption")
				}
				wire.Reset()
				if outcome == "rejected" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%s,"error":{"code":-1,"message":"rejected"}}`, requestID))
					if u.interruption.beforeStart || d.active() {
						t.Fatal("rejected start retained deferred interruption")
					}
					resetDriverRequireMethods(t, wire)
				} else {
					appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"`+d.thread+`","turn":{"id":"next"}}}`)
					resetDriverRequireMethods(t, wire, "turn/interrupt")
				}
			})
		}
	}
}
