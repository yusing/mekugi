package binding

import (
	"cmp"

	"github.com/yusing/mekugi/contracts/observation"
)

// TranscriptItem is the decoded native item's text subset. Observation excludes
// raw/private reasoning; user content remains with the Conversation consumer.
type TranscriptItem struct {
	ID, Type, Text, Status, Delivery, Phase string
	Summary                                 []string
	Questions                               []Question
}

func (i TranscriptItem) Observation(thread, turn, id string) observation.TextItem {
	return observation.TextItem{Ref: observation.ItemRef{ThreadID: thread, TurnID: turn, ItemID: cmp.Or(id, i.ID)}, Type: i.Type, Text: authoredTranscriptText(i.Type, i.Text), Status: i.Status, Summary: i.Summary,
		AsyncQuestion: i.Delivery == "async" && len(i.Questions) > 0, Final: i.Phase == "final_answer" || i.Phase == "finalAnswer"}
}

// RetainedTranscript preserves already decoded rollout values directly; it does
// not reconstruct a notification or interpret unavailable raw reasoning.
func RetainedTranscript(e observation.Event) observation.TextEvent {
	i := e.Item
	return observation.TextEvent{At: e.At, Kind: e.Kind, Delta: e.Delta, Item: observation.TextItem{
		Ref: observation.ItemRef{ThreadID: e.ThreadID, TurnID: e.TurnID, ItemID: cmp.Or(e.ItemID, i.ID)}, Type: i.Type, Text: authoredTranscriptText(i.Type, i.Text), Status: i.Status, Summary: i.Summary,
		AsyncQuestion: i.Delivery == "async" && len(i.Questions) > 0, Final: i.Phase == "final_answer" || i.Phase == "finalAnswer"}}
}

// Only assistant-message text is authored transcript content here. Reasoning
// contributes its public Summary; user content belongs to Conversation.
func authoredTranscriptText(kind, text string) string {
	if kind == "agentMessage" {
		return text
	}
	return ""
}
