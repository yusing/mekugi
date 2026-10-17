package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func (u *appServerUI) observeTranscript(e observation.TextEvent, main bool) []observation.TextUpdate {
	s := &u.session
	ref := e.Item.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return nil
	}
	return u.transcript.Observe(e, main, s.Transcript(ref.ThreadID, ref.TurnID))
}

func (u *appServerUI) providerStarted(thread string, at time.Time) []observation.TextUpdate {
	s := &u.session
	if thread == "" {
		return nil
	}
	return u.transcript.ProviderStarted(thread, at, s.Thread(thread))
}

// Host snapshots retain their original payload for control and evidence; this
// projection supplies only text facts to the shared transcript owner.
func transcriptItem(item binding.ThreadItem, thread, turn, id string) observation.TextItem {
	return (binding.TranscriptItem{ID: item.ID, Type: item.Type, Text: item.Text, Status: item.Status, Summary: item.Summary, Delivery: item.Delivery, Phase: item.Phase, Questions: item.Questions}).Observation(thread, turn, id)
}

func (u *appServerUI) transcriptUpdates(updates []observation.TextUpdate, paced bool) []sessionpresentation.ActivityEntry {
	for i := range updates {
		updates[i].Sequence = u.session.Activity().NextSequence()
	}
	if paced {
		updates = u.streams.Reasoning.Queue(updates)
	}
	entries := make([]sessionpresentation.ActivityEntry, 0, len(updates))
	for _, update := range updates {
		agent := "/root"
		if update.Ref.ThreadID != u.Thread {
			agent = u.session.Caller(update.Ref.ThreadID)
		}
		entries = append(entries, sessionpresentation.TranscriptEntry(update, agent))
	}
	return entries
}

func historicalTranscript(items []binding.ThreadItem, thread, turn string, main, promote bool, at time.Time) map[string]observation.TextUpdate {
	events := make([]observation.TextEvent, len(items))
	for i, item := range items {
		events[i] = observation.TextEvent{At: at, Kind: observation.ItemCompleted, Item: transcriptItem(item, thread, turn, item.ID)}
	}
	return transcript.History(events, main, promote)
}

func (u *appServerUI) flushSummaryOutput() {
	updates := u.streams.Reasoning.Advance(u.now())
	if len(updates) == 0 {
		return
	}
	u.applyActivity(u.transcriptUpdates(updates, false), nil)
	u.dirty = true
}

// observeMessageQuestions is the single adapter for live, restored and playback
// message admission. Parking precedes the model transition so external replies
// cannot discard an active draft before it reaches the question's editor.
func (u *appServerUI) observeMessageQuestions(text observation.TextEvent, item observation.Item, history bool) bool {
	var userText string
	if item.Type == "userMessage" {
		userText, _, _ = sessionpresentation.UserText(item.Content)
	}
	in := conversationbinding.MessageQuestions(text, item, userText, text.Item.Ref.ThreadID == u.Thread, history)
	if u.questions.NeedsParking(in) {
		u.hideQuestions()
	}
	changes := u.questions.ObserveMessage(in)
	if changes.Added != nil {
		u.bindQuestion(&nativeQuestionCall{Question: &sessioncontrol.Question{Call: changes.Added}}, history)
	}
	for _, change := range changes.Changed {
		for _, draft := range change.Discarded {
			u.saveUnsentAnswer(draft)
		}
		c := u.questions.handles[change.Call]
		if change.Superseded {
			u.discardQuestionEditors(c)
		}
		u.renderQuestionRecord(c)
	}
	return changes.Suppress
}

func (u *appServerUI) messageTranscript(text observation.TextEvent, item observation.Item, main bool) bool {
	if u.observeMessageQuestions(text, item, false) {
		return true
	}
	updates := u.observeTranscript(text, main)
	if !main {
		u.applyActivity(u.transcriptUpdates(updates, true), nil)
		return false
	}
	for _, update := range updates {
		if update.Kind == "reasoning" {
			u.applyActivity(u.transcriptUpdates([]observation.TextUpdate{update}, true), nil)
			continue
		}
		u.source.ApplyTranscript(update, item.Content, u.cwd, u.Thread, item.Type == "userMessage")
	}
	return false
}

// replayTranscript adapts retained text without live input or request settlement.
func (u *appServerUI) replayTranscript(e observation.Event) bool {
	text := binding.RetainedTranscript(e)
	if e.Kind != observation.ReasoningDelta && e.Kind != observation.ReasoningPart && e.Kind != observation.AgentDelta && e.Item.Type != "reasoning" && e.Item.Type != "agentMessage" && e.Item.Type != "userMessage" {
		return false
	}
	if u.messageTranscript(text, e.Item, e.ThreadID == u.Thread) {
		return true
	}
	// Replay shares Main's progress identity and never revives operations.
	u.sessionPresentation().ObserveProgress(e, u.Turn, false)
	return true
}
