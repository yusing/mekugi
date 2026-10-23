package presentation

import (
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"testing"
)

func TestSteerQueuedPreservesBoundInputAndSpecialEntries(t *testing.T) {
	m := new(Session)
	m.QueueInput(Input{Draft: composer.Draft{Text: "already waiting"}}, false)
	for _, part := range []Input{{Draft: composer.Draft{Text: "next", Attachments: []string{"snapshot"}}, Origin: "browser", Delivery: 7}, {Draft: composer.Draft{Text: "/compact"}}, {Draft: composer.Draft{Text: "answer"}, Question: &prompt.Call{}}} {
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
