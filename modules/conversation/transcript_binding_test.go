package conversation_test

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestTranscriptBindingUsesApplicationScopes(t *testing.T) {
	service := new(presentation.Service)
	binding := conversation.Module.Bindings.Observation(service)
	var first, second scope.Store
	firstStores := observation.TextScope{Thread: new(scope.Store), Turn: new(scope.Store)}
	secondStores := observation.TextScope{Thread: new(scope.Store), Turn: new(scope.Store)}
	live := binding.NewTranscript(&first)
	e := observation.TextEvent{At: time.Unix(100, 0), Kind: observation.ReasoningDelta, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: "thread", TurnID: "turn", ItemID: "summary"}}, Delta: "first"}
	live.Observe(e, true, firstStores)
	other := binding.NewTranscript(&second)
	e.Delta = " separate"
	if got := other.Observe(e, true, secondStores); len(got) != 1 || got[0].Text != " separate" {
		t.Fatalf("new attachment inherited text: %+v", got)
	}
	e.Delta = " continued"
	if got := binding.NewTranscript(&first).Observe(e, true, firstStores); len(got) != 1 || got[0].Text != "first continued" {
		t.Fatalf("binding replaced an existing scope: %+v", got)
	}
	first = scope.Store{}
	firstStores = observation.TextScope{Thread: new(scope.Store), Turn: new(scope.Store)}
	reset := binding.NewTranscript(&first)
	e.Delta = " reset"
	if got := reset.Observe(e, true, firstStores); len(got) != 1 || got[0].Text != " reset" {
		t.Fatalf("reset retained text: %+v", got)
	}
	e.Delta = " kept"
	if got := other.Observe(e, true, secondStores); len(got) != 1 || got[0].Text != " separate kept" {
		t.Fatalf("reset changed independent attachment: %+v", got)
	}
}
