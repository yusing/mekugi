package presentation

import (
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"testing"
)

func TestSteerQueuedPreservesBoundInputAndSpecialEntries(t *testing.T) {
	m := new(Session)
	m.QueueInput(Input{Text: "already waiting"}, false)
	for _, part := range []Input{{Text: "next", Attachments: []string{"snapshot"}, Origin: "browser", Delivery: 7}, {Text: "/compact", Compact: new(CompactIntent{})}, {Text: "answer", Question: &prompt.Call{}}} {
		m.QueueInput(part, true)
	}
	if !m.Queued[0].NextTurn || !m.Pending(nil, Waiting).CanSteer || !m.SteerQueued() {
		t.Fatal("queued input not promoted")
	}
	if len(m.Unsent) != 2 || m.Unsent[1].NextTurn || m.Unsent[1].Origin != "browser" || m.Unsent[1].Delivery != 7 || m.Unsent[1].Attachments[0] != "snapshot" {
		t.Fatalf("promotion changed retained input: %+v", m.Unsent)
	}
	if len(m.Queued) != 2 || m.Queued[0].Text != "/compact" || m.Queued[1].Question == nil || (m.SteerQueued() || m.Pending(nil, Waiting).CanSteer) {
		t.Fatal("promoted special entries or repeated a message")
	}
}

func TestLiteralCompactInputBatchesAndSteersAsMessage(t *testing.T) {
	literal := Input{Text: "/compact"}
	command := Input{Text: "/compact", Compact: new(CompactIntent{})}
	batch, rest := Batch([]Input{literal, {Text: "next message"}, command, {Text: "later"}})
	if len(batch) != 2 || len(rest) != 2 || rest[0].Compact == nil {
		t.Fatalf("literal message changed command boundaries: batch=%+v rest=%+v", batch, rest)
	}
	batch, rest = Batch(rest)
	if len(batch) != 1 || batch[0].Compact == nil || len(rest) != 1 {
		t.Fatal("semantic compaction command joined ordinary input")
	}
	m := new(Session)
	m.QueueInput(literal, true)
	m.QueueInput(command, true)
	if !m.SteerQueued() || len(m.Unsent) != 1 || m.Unsent[0].Compact != nil || len(m.Queued) != 1 || m.Queued[0].Compact == nil {
		t.Fatal("literal text was treated as a non-steerable command")
	}
}
