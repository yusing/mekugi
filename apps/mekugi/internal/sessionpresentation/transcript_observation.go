package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_transcript.go:18:26,41:64@7166de95.
import "github.com/yusing/mekugi/contracts/observation"

func (s *Attachment) ObserveText(transcript observation.Transcript, e observation.TextEvent, main bool) []observation.TextUpdate {
	ref := e.Item.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return nil
	}
	return transcript.Observe(e, main, s.Transcript(ref.ThreadID, ref.TurnID))
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
