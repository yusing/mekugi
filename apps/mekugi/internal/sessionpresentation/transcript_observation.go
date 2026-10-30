package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_transcript.go:18:26,41:64@7166de95.
import (
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

type completionStreams map[string]*compact.ReloadStream

// ProjectCompletionText keeps terminal protocol metadata out of retained and
// live answer projections without modifying native control/evidence payloads.
func ProjectCompletionText(e observation.TextEvent) observation.TextEvent {
	if e.Item.Type == "agentMessage" && e.Kind == observation.ItemCompleted {
		e.Item.Text = compact.ReloadText(e.Item.Text)
	}
	return e
}

func (s *Attachment) ObserveText(transcript observation.Transcript, e observation.TextEvent, main bool) []observation.TextUpdate {
	ref := e.Item.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return nil
	}
	stores := s.Transcript(ref.ThreadID, ref.TurnID)
	if stores.Turn != nil {
		streams := s.completion.Value(stores.Turn)
		if e.Kind == observation.AgentDelta && main {
			if *streams == nil {
				*streams = make(completionStreams)
			}
			if (*streams)[ref.ItemID] == nil {
				(*streams)[ref.ItemID] = new(compact.ReloadStream)
			}
			e.Delta = (*streams)[ref.ItemID].Push(e.Delta)
		} else if e.Kind == observation.ItemCompleted {
			delete(*streams, ref.ItemID)
		}
	}
	return transcript.Observe(ProjectCompletionText(e), main, stores)
}

func (s *Attachment) AdmitText(updates []observation.TextUpdate, main string) []ActivityEntry {
	for i := range updates {
		updates[i].Sequence = s.Activity().NextSequence()
		if updates[i].Kind == "reasoning" && updates[i].Phase == "item/completed" {
			updates[i].Completed = updates[i].Observed
		}
	}
	entries := make([]ActivityEntry, 0, len(updates))
	for _, update := range updates {
		agent := "/root"
		if update.Ref.ThreadID != main {
			agent = s.Caller(update.Ref.ThreadID)
		}
		entries = append(entries, TranscriptEntry(update, agent))
	}
	return entries
}

// ProjectMessage admits already-classified text after Conversation question handling.
// Live and recorded consumers share source ordering and public-reasoning delivery.
// Source: apps/mekugi/internal/router/app_server_transcript.go:92:113@4b40d245.
func ProjectMessage(s *Attachment, mainSource, childSource *ActivitySource, transcript observation.Transcript, main bool, in ObservationInput) (deliveries []ActivityDelivery) {
	updates := s.ObserveText(transcript, in.Text, main)
	if !main {
		ApplyActivity(mainSource, childSource, s.AdmitText(updates, in.Main), s.Roster())
		for _, update := range updates {
			deliveries = append(deliveries, ActivityDelivery{Text: new(update)})
		}
		return
	}
	for _, update := range updates {
		if update.Kind == "reasoning" {
			values := []observation.TextUpdate{update}
			ApplyActivity(mainSource, childSource, s.AdmitText(values, in.Main), nil)
			deliveries = append(deliveries, ActivityDelivery{Text: new(values[0])})
		} else {
			mainSource.ApplyTranscript(update, in.Event.Item.Content, in.Workspace, in.Main, in.Event.Item.Type == "userMessage")
		}
	}
	return
}
