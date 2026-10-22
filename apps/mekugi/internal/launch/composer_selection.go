package launch

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func referenceInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, parent context.Context, intent sessionpresentation.ReferenceIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	if attached.effects != nil {
		if err := attached.presentation.ClientReference(intent); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false
		}
		pending := slices.Concat(attached.conversation.Submitted[control.Submission().ID].Parts, attached.conversation.Unsent, attached.conversation.Queued, attached.conversation.Parts(control.Steers()))
		// Selection labels only depend on pending text, never its attachment
		// snapshots. Keep those payloads with their actual input owners.
		drafts := make([]composer.Draft, len(pending))
		for i, input := range pending {
			drafts[i].Text = input.Text
		}
		thread := intent.Thread
		if thread == "" {
			thread = attached.presentation.View().Input.ID
		}
		attached.effects.add(sessionapi.NativeEffect{Thread: thread, Target: new(intent.Target), Selection: &sessionapi.NativeSelection{Description: intent.Description, Text: intent.Text, Source: intent.Source, Pending: drafts}})
		return true
	}
	if err := attached.presentation.Reference(intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	insertSelection(attached, terminal, control, editor, questions, approvals, clock, side, owned, intent.Description, intent.Text, intent.Source)
	picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	(*editor).Run = composer.NoRun
	return true
}

// composer.Token is a screen selection mentioned as one composer token.
// The draft shows only its concise label; the quoted text travels in the
// submitted attachment envelope, so history, forks and resume keep it.

// insertSelection mentions text at the caret as one undoable token. A label
// already in the draft or pending input gains a counter, so the queued, steered
// or restored input that joins them still names one quote per mention.
func insertSelection(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, description, text, source string) {
	if text == "" {
		return
	}
	pending := slices.Concat(attached.conversation.Submitted[control.Submission().ID].Parts, attached.conversation.Unsent, attached.conversation.Queued, attached.conversation.Parts(control.Steers()))
	for _, call := range questions.calls() {
		for i, question := range call.Questions {
			draft := question.Editor.Snapshot.Draft
			if question.IsSecret {
				draft = terminal.Prompts.SecretState(call.ID).Editors[i].Snapshot.Draft
			}
			pending = append(pending, conversationmodel.Input{Draft: draft})
		}
	}
	if questions.Current() != nil {
		pending = append(pending, conversationmodel.Input{Draft: attached.conversation.PromptEditor.Parked.Snapshot.Draft})
	}
	if err := (*editor).InsertSelection(description, text, source, conversationmodel.Drafts(pending)); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	terminal.Main.CursorColumn = nil
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side), attached.nativeImages)
	if q := questions.Current(); q != nil {
		q.Edit(!q.Note)
	}
}
