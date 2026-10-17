package router

import (
	"encoding/json/jsontext"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func (s *appServerSession) observeTranscript(e observation.TextEvent, main bool) []observation.TextUpdate {
	ref := e.Item.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return nil
	}
	return s.transcript.Observe(e, main, s.Transcript(ref.ThreadID, ref.TurnID))
}

func (s *appServerSession) providerStarted(thread string, at time.Time) []observation.TextUpdate {
	if thread == "" {
		return nil
	}
	return s.transcript.ProviderStarted(thread, at, s.Thread(thread))
}

// Host snapshots retain their original payload for control and evidence; this
// projection supplies only text facts to the shared transcript owner.
func transcriptItem(item binding.ThreadItem, thread, turn, id string) observation.TextItem {
	return (binding.TranscriptItem{ID: item.ID, Type: item.Type, Text: item.Text, Status: item.Status, Summary: item.Summary, Delivery: item.Delivery, Phase: item.Phase, Questions: item.Questions}).Observation(thread, turn, id)
}

func transcriptEntry(update observation.TextUpdate, agent string) activityPaneEntry {
	call := ""
	if update.Kind == "reasoning" {
		call = update.Ref.ItemID
	}
	return activityPaneEntry{Seq: update.Sequence, Agent: agent, Kind: update.Kind, Text: update.Text, CallID: call, Observed: update.Observed, completed: update.Completed,
		native: &liveActivityNativeItem{thread: update.Ref.ThreadID, turn: update.Ref.TurnID, item: update.Ref.ItemID, phase: update.Phase, status: update.Status, live: update.Live, thought: update.Thought, replaces: update.Replaces}}
}

func (u *appServerUI) transcriptUpdates(updates []observation.TextUpdate, paced bool) []activityPaneEntry {
	for i := range updates {
		updates[i].Sequence = u.session.next()
	}
	if paced {
		updates = u.summaryStream.Queue(updates)
	}
	entries := make([]activityPaneEntry, 0, len(updates))
	for _, update := range updates {
		agent := "/root"
		if update.Ref.ThreadID != u.Thread {
			agent = u.session.path(update.Ref.ThreadID)
		}
		entries = append(entries, transcriptEntry(update, agent))
	}
	return entries
}

// applyTranscript enriches an existing feed with Conversation's user/span and
// attachment interpretation. It owns no host lifecycle or message accumulation.
func (v *activityAdapter) applyTranscript(update observation.TextUpdate, content jsontext.Value, cwd, main string, user bool) {
	ref := update.Ref
	if ref.ThreadID == "" || ref.TurnID == "" || ref.ItemID == "" {
		return
	}
	agent := "Main"
	if ref.ThreadID != main {
		agent = "Thread " + ref.ThreadID
	}
	entry := transcriptEntry(update, agent)
	entry.Seq = v.LastSeq + 1
	if user {
		var ok bool
		if entry.Text, entry.native.spans, ok = appServerUserText(content); !ok {
			return
		}
		if replies := binding.QuestionReplies(entry.Text); len(replies) > 0 {
			entry.Kind, entry.native.replySource = "question_reply", entry.Text
			for _, reply := range replies {
				questionID, _, ok := binding.QuestionReplyIdentity(reply)
				if ok && entry.native.question == 0 {
					for _, question := range v.Entries {
						if question.Source.Kind == "question" && question.Source.native != nil && question.Source.native.thread == ref.ThreadID && question.Source.native.item == questionID {
							entry.native.question = question.Source.Seq
							break
						}
					}
				}
			}
			entry.Text = questionReplyText(replies)
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
			return
		}
		if ref.ThreadID == main {
			entry.Agent = "You"
		} else {
			entry.Agent += " · user"
		}
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	if user {
		workspace := ""
		if ref.ThreadID == main {
			workspace = cwd
		}
		if blocks := appServerAttachmentBlocks(workspace, content); len(blocks) > 0 {
			entry.Agent = agent
			entry.Seq, entry.Kind, entry.Text = v.LastSeq+1, "attachments", ""
			entry.native = &liveActivityNativeItem{thread: ref.ThreadID, turn: ref.TurnID, item: ref.ItemID + "/attachments", phase: update.Phase, live: update.Live, attachments: blocks}
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
	}
}

func historicalTranscript(items []binding.ThreadItem, thread, turn string, main, promote bool, at time.Time) map[string]observation.TextUpdate {
	events := make([]observation.TextEvent, len(items))
	for i, item := range items {
		events[i] = observation.TextEvent{At: at, Kind: observation.ItemCompleted, Item: transcriptItem(item, thread, turn, item.ID)}
	}
	return transcript.History(events, main, promote)
}

func (u *appServerUI) flushSummaryOutput() {
	updates := u.summaryStream.Advance(u.now())
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
		userText, _, _ = appServerUserText(item.Content)
	}
	in := conversationbinding.MessageQuestions(text, item, userText, text.Item.Ref.ThreadID == u.Thread, history)
	if u.questions.NeedsParking(in) {
		u.hideQuestions()
	}
	changes := u.questions.ObserveMessage(in)
	if changes.Added != nil {
		u.bindQuestion(&nativeQuestionCall{Call: changes.Added}, history)
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
	updates := u.session.observeTranscript(text, main)
	if !main {
		u.applyActivity(u.transcriptUpdates(updates, true), nil)
		return false
	}
	for _, update := range updates {
		if update.Kind == "reasoning" {
			u.applyActivity(u.transcriptUpdates([]observation.TextUpdate{update}, true), nil)
			continue
		}
		u.view.applyTranscript(update, item.Content, u.session.cwd, u.Thread, item.Type == "userMessage")
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
