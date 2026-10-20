package binding

import (
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestTranscriptProjectionPublicFactsAndRetainedParity(t *testing.T) {
	at := time.Unix(100, 0)
	source := TranscriptItem{ID: "summary", Type: "reasoning", Text: "PRIVATE", Summary: []string{"Public", "Second"}}
	projected := observation.TextEvent{At: at, Kind: observation.ItemCompleted, Item: source.Observation("child", "turn", "")}
	want := observation.TextEvent{At: at, Kind: observation.ItemCompleted, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "summary"}, Type: "reasoning", Summary: []string{"Public", "Second"}}}
	if !reflect.DeepEqual(projected, want) || source.Text != "PRIVATE" {
		t.Fatalf("projection changed source or public facts: %+v", projected)
	}
	retained := RetainedTranscript(observation.Event{At: at, Kind: observation.ItemCompleted, ThreadID: "child", TurnID: "turn", Item: observation.Item{ID: "summary", Type: "reasoning", Summary: []string{"Public", "Second"}}})
	if !reflect.DeepEqual(retained, projected) {
		t.Fatalf("retained/live divergence: %+v != %+v", retained, projected)
	}
}

func TestTranscriptProjectionAsyncAndFinalIdentity(t *testing.T) {
	i := TranscriptItem{ID: "item", Type: "agentMessage", Delivery: "async", Phase: "finalAnswer", Questions: []Question{{ID: "q"}}}
	fact := i.Observation("thread", "turn", "override")
	if fact.Ref.ItemID != "override" || !fact.AsyncQuestion || !fact.Final {
		t.Fatalf("item facts: %+v", fact)
	}
}
