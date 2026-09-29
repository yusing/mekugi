package router

import (
	"testing"
	"time"
)

func TestJournalResetDriverFastContinuationDefersNextSliceUntilAcknowledged(t *testing.T) {
	d, wire := resetDriverFixture(t, "off")
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Third")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(time.Unix(102, 0)); err != nil {
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
	resetDriverRequireMethods(t, wire, "turn/start")
	intent, err = d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Turn != "continued" || intent.Path != "/3" {
		t.Fatalf("deferred next slice not processed: %+v %v", intent, err)
	}
}
