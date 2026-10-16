package terminalview

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestReasoningStreamReservesAndDrainsCompletion(t *testing.T) {
	at := time.Unix(100, 0)
	body := strings.Repeat("A public summary line.\n", 20)
	entry := observation.TextUpdate{Ref: observation.ItemRef{ThreadID: "main", TurnID: "turn", ItemID: "summary"}, Kind: "reasoning", Phase: "summary", Text: body, Observed: at, Live: true, Sequence: 10}
	var stream ReasoningStream
	reserved := stream.Queue([]observation.TextUpdate{entry})
	if len(reserved) != 1 || reserved[0].Text != "" || reserved[0].Phase != "queued-summary" {
		t.Fatalf("reservation: %+v", reserved)
	}
	first := stream.Advance(at)
	if len(first) != 1 || first[0].Text == body || first[0].Text == "" {
		t.Fatalf("unpaced first frame: %+v", first)
	}
	entry.Phase, entry.Thought, entry.Sequence = "item/completed", 2*time.Second, 11
	if shown := stream.Queue([]observation.TextUpdate{entry}); len(shown) != 0 {
		t.Fatalf("completion bypassed backlog: %+v", shown)
	}
	var done observation.TextUpdate
	for i := range 100 {
		for _, update := range stream.Advance(at.Add(time.Duration(i) * time.Second)) {
			if update.Phase == "item/completed" {
				done = update
			}
		}
		if done.Phase != "" {
			break
		}
	}
	if done.Text != body || done.Thought != 2*time.Second || done.Completed.IsZero() {
		t.Fatalf("completion lost source or painted time: %+v", done)
	}
	if updates := stream.Advance(at.Add(time.Hour)); len(updates) != 0 {
		t.Fatalf("completed stream reopened: %+v", updates)
	}
}

func TestReasoningStreamSeekSettlesOnlyCompletedAndKeepsOrder(t *testing.T) {
	at := time.Unix(100, 0)
	var stream ReasoningStream
	for _, seq := range []uint64{20, 10} {
		entry := observation.TextUpdate{Ref: observation.ItemRef{ThreadID: "main", ItemID: string(rune(seq))}, Kind: "reasoning", Phase: "item/completed", Text: strings.Repeat("line\n", 40), Live: true, Sequence: seq}
		stream.Queue([]observation.TextUpdate{entry})
	}
	stream.FinishCompleted()
	updates := stream.Advance(at)
	if len(updates) != 4 || updates[0].Sequence != 10 || updates[1].Sequence != 10 || updates[2].Sequence != 20 || updates[3].Sequence != 20 {
		t.Fatalf("completion order: %+v", updates)
	}
	if updates[1].Completed != at || updates[3].Completed != at {
		t.Fatal("seek used wall time")
	}
	var reset ReasoningStream
	if updates := reset.Advance(at); len(updates) != 0 {
		t.Fatalf("reset retained future text: %+v", updates)
	}
}
