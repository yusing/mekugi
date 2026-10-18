package launch

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func providerStarted(attached *attachedPresentation, thread string, at time.Time) []observation.TextUpdate {
	s := &attached.session
	if thread == "" {
		return nil
	}
	return attached.transcript.ProviderStarted(thread, at, s.Thread(thread))
}

// Host snapshots retain their original payload for control and evidence; this
// projection supplies only text facts to the shared transcript owner.
func transcriptItem(item binding.ThreadItem, thread, turn, id string) observation.TextItem {
	return (binding.TranscriptItem{ID: item.ID, Type: item.Type, Text: item.Text, Status: item.Status, Summary: item.Summary, Delivery: item.Delivery, Phase: item.Phase, Questions: item.Questions}).Observation(thread, turn, id)
}

func transcriptUpdates(attached *attachedPresentation, terminal *terminalsession.Attachment, streams *terminalsession.Streams, main string, updates []observation.TextUpdate, paced bool) []sessionpresentation.ActivityEntry {
	entries := attached.session.AdmitText(updates, main)
	if paced {
		streams.QueueText(slices.Clone(updates), terminal.Transcript, terminal.Agents)
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

// observeMessageQuestions binds live and restored message admission. Playback
// uses the same Conversation reducer without response handles. Parking precedes
// the model transition so external replies cannot discard an active draft before
// it reaches the question's editor.
func observeMessageQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], approvals *nativeApprovalDock, clock func() time.Time, questions *nativeQuestionDock, editor **composer.Editor, picker *composerPicker, overlay bool, notifications *nativeNotifications, text observation.TextEvent, item observation.Item, history bool) bool {
	var userText string
	if item.Type == "userMessage" {
		userText, _, _ = sessionpresentation.UserText(item.Content)
	}
	in := conversationbinding.MessageQuestions(text, item, userText, text.Item.Ref.ThreadID == control.Thread, history)
	if questions.NeedsParking(in) {
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	}
	changes := questions.ObserveMessage(in)
	if changes.Added != nil {
		bindQuestion(attached, terminal, control, picker, editor, questions, approvals, clock, overlay, notifications, &sessioncontrol.Question{Call: changes.Added}, history)
	}
	for _, change := range changes.Changed {
		for _, draft := range change.Discarded {
			saveUnsentAnswer(attached, terminal, clock, draft)
		}
		c := questions.handles[change.Call]
		if change.Superseded {
			discardQuestionEditors(terminal.Prompts, c)
		}
		renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
	}
	return changes.Suppress
}

func messageTranscript(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], streams *terminalsession.Streams, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, questions *nativeQuestionDock, editor **composer.Editor, picker *composerPicker, overlay bool, notifications *nativeNotifications, text observation.TextEvent, item observation.Item, main bool) bool {
	if observeMessageQuestions(attached, terminal, control, approvals, clock, questions, editor, picker, overlay, notifications, text, item, false) {
		return true
	}
	deliveries := sessionpresentation.ProjectMessage(&attached.session, attached.source, attached.childSource, attached.transcript, main, sessionpresentation.ObservationInput{
		Event: observation.Event{ThreadID: text.Item.Ref.ThreadID, Item: item}, Text: text, Main: control.Thread, Workspace: attached.cwd,
	})
	streams.Deliver(deliveries, terminal.Transcript, terminal.Agents)
	if !main || len(deliveries) > 0 {
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, nil, nil)
	}
	return false
}
