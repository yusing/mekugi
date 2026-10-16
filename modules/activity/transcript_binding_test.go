package activity_test

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/activity/feed"
)

func TestTranscriptBindingResetsOnlyItsAttachment(t *testing.T) {
	for _, service := range []*feed.Service{new(feed.Service), nil} {
		binding := activity.Module.Bindings.Observation(service)
		var first, second scope.Store
		live := binding.NewTranscript(&first)
		e := observation.TextEvent{At: time.Unix(100, 0), Kind: observation.ReasoningDelta, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: "thread", TurnID: "turn", ItemID: "summary"}}, Delta: "first"}
		live.Observe(e, true)
		other := binding.NewTranscript(&second)
		e.Delta = " separate"
		if got := other.Observe(e, true); len(got) != 1 || got[0].Text != " separate" {
			t.Fatalf("new attachment inherited text: %+v", got)
		}
		reset := binding.NewTranscript(&first)
		e.Delta = " reset"
		if got := reset.Observe(e, true); len(got) != 1 || got[0].Text != " reset" {
			t.Fatalf("reset retained text: %+v", got)
		}
		e.Delta = " kept"
		if got := other.Observe(e, true); len(got) != 1 || got[0].Text != " separate kept" {
			t.Fatalf("reset changed independent attachment: %+v", got)
		}
	}
}
