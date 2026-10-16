package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestTranscriptCompletionReleasesReasoningBody(t *testing.T) {
	for _, completion := range []string{"item", "turn", "completion-only"} {
		t.Run(completion, func(t *testing.T) {
			var state State
			at := time.Unix(100, 0)
			body := strings.Repeat("A retained public summary line.\n", 128)
			ref := observation.ItemRef{ThreadID: "child", TurnID: "turn", ItemID: "summary"}
			state.ProviderStarted(ref.ThreadID, at)
			event := observation.TextEvent{At: at.Add(time.Second), Kind: observation.ReasoningDelta, Item: observation.TextItem{Ref: ref, Type: "reasoning"}, Delta: body}
			if completion != "completion-only" {
				state.Observe(event, false)
			}
			event.At = at.Add(3 * time.Second)
			var updates []observation.TextUpdate
			if completion == "turn" {
				updates = state.TurnCompleted(ref.ThreadID, ref.TurnID, "completed", event.At)
			} else {
				event.Kind = observation.ItemCompleted
				if completion == "completion-only" {
					event.Item.Summary = []string{body}
				}
				updates = state.Observe(event, false)
			}
			if len(updates) != 1 || updates[0].Ref != ref || updates[0].Phase != "item/completed" || updates[0].Text != body || updates[0].Thought != 3*time.Second {
				t.Fatalf("completion lost emitted body, identity or duration: %+v", updates)
			}
			if retained, exists := state.reasoning[ref]; !exists || retained != "" {
				t.Fatalf("completed identity must retain only a tombstone: exists=%v retained bytes=%d", exists, len(retained))
			}
			event.Kind = observation.ReasoningPart
			state.Observe(event, false)
			event.Kind, event.Delta = observation.ReasoningDelta, "late body"
			if late := state.Observe(event, false); len(late) != 0 {
				t.Fatalf("late delta reopened completion: %+v", late)
			}
			if retained := state.reasoning[ref]; retained != "" || len(state.thinking) != 0 {
				t.Fatalf("late update revived state: retained bytes=%d active=%d", len(retained), len(state.thinking))
			}
			if updates[0].Text != body {
				t.Fatal("retiring correlation invalidated the emitted body")
			}
		})
	}
}
