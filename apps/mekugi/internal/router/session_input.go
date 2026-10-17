package router

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func editInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, intent sessionpresentation.DraftEditIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	if err := attached.presentation.EditDraft(intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages(owned, (*editor), attached.conversation, questions, approvals, terminal.Prompts, side)
	return true
}

func submitInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessionpresentation.SubmitIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	draft, plan, err := attached.presentation.TakeInput(intent, &control.Live, attached.conversation, restoring)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	input := prepareDraft(picker, terminal, attached.cwd, environment, draft)
	attached.conversation.QueueInput(input, plan.Queued)
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side))
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	if !plan.Flush {
		return nil
	}
	terminal.Transcript.Follow()
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
}

// applyComposerInput resolves terminal output through the current owners. Every
// semantic action retains the publication and editor target it came from.
func (u *appServerUI) applyComposerInput(input terminalsession.ComposerInput) (bool, error) {
	if input.Err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), input.Err.Error(), true)
		return false, nil
	}
	if input.Command.Operation != sessionpresentation.NoComposerOperation {
		return u.applyComposerCommand(input.Command)
	}
	kind, changed := input.LocalEdit, input.Changed
	if input.Editor.Action.Kind != composer.NoAction {
		var err error
		changed, err = u.attached.presentation.ApplyEditor(input.Editor, u.attached.conversation)
		if err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
			return false, nil
		}
		kind = input.Editor.Action.Kind
	}
	switch kind {
	case composer.NoAction, composer.MoveAction:
		return false, nil
	case composer.RecallPreviousAction, composer.RecallNextAction:
		if !changed {
			return false, nil
		}
		u.shell.Main.CursorColumn = nil
	case composer.DeleteAction:
		if !changed {
			return false, nil
		}
	}
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	if kind != composer.UndoAction && kind != composer.RedoAction {
		pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
	}
	return false, nil
}

func (u *appServerUI) applyComposerCommand(intent sessionpresentation.ComposerIntent) (bool, error) {
	if err := u.attached.presentation.AdmitComposer(intent, u.attached.conversation); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return false, nil
	}
	switch intent.Operation {
	case sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort:
		return false, stepReasoning(&u.attached, u.shell, &u.Client, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.serviceTiers, u.panes, &u.dirty, u.restoring != nil, u.clock, u.models, u.modelsLoading, &u.reasoningKey, intent.Operation == sessionpresentation.IncreaseEffort)
	case sessionpresentation.RestoreWaiting:
		editQueued(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock)
	case sessionpresentation.EditExternally:
		if q := u.questions.Current(); q != nil && q.IsSecret {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "External editor is unavailable for secret answers", false)
			return false, nil
		}
		return false, &composerEditorRequest{intent: intent}
	case sessionpresentation.AttachClipboard:
		pasteImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, u.ctx, intent)
	case sessionpresentation.CancelComposer:
		if u.Replacement.Archiving() {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Archiving session…", false)
			return false, nil
		}
		// As in Codex, Ctrl-C first clears the draft (undoable with Ctrl+Z),
		// then cancels queued compaction, interrupts the active turn, or quits.
		if u.Text != "" {
			deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if u.Turn != "" || u.Starting() || u.InputPending() {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again interrupts", false)
			}
			if u.shell.Main.InterruptLocked {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Locked · /unlock", false)
			}
			return false, nil
		}
		if cancelQueuedCompact(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock) {
			return false, nil
		}
		if u.Turn != "" || u.Starting() || u.InputPending() || u.Compaction.Pending() || u.Compaction.ContinuationPending() || len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) > 0 {
			return false, keyboardInterrupt(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts)
		}
		if u.shell.Main.InterruptLocked {
			lockNotice(&u.attached, u.shell, u.clock)
			return false, nil
		}
		return true, nil
	case sessionpresentation.SubmitComposer:
		if u.Replacement.Archiving() {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace(u.Text)
		if text == "/archive" {
			showArchivePicker(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages)
			return false, nil
		}
		if handled, err := controlsCommand(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, text); handled {
			return false, err
		}
		if titleCommand(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.titleGenerator, &u.dirty, text) {
			return false, nil
		}
		if text == "/btw" || strings.HasPrefix(text, "/btw ") || strings.HasPrefix(text, "/btw\n") || strings.HasPrefix(text, "/btw\t") {
			return false, submitBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment)
		}
		if u.attached.conversation.ShellMode() {
			return false, submitShell(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane)
		}
		if text == "/compact" || text == "/clear" {
			return false, sessionCommand(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.restoring != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, u.statusConfig.Provider, u.approvalMode, text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, resumeCommand(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, &u.restoring, &u.resumePicker, u.skillEnvironment, u.approvalMode, text)
		}
		if text == "/ps" {
			showRunningProcesses(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, &u.detailFiles)
			return false, nil
		}
		if text == "/session" {
			showSessionMetrics(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, &u.dirty, u.sessionCapture, u.replayDebugDirectory)
			return false, nil
		}
		if text == "/status" {
			return false, showStatus(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, &u.dirty, u.statusConfig, u.replayDebugDirectory)
		}
		if text == "/copy" {
			showCopyPicker(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages)
			return false, nil
		}
		if text == "/skills" {
			deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
			u.picker.openMenu("menu")
			u.picker.Choices = []completion.Choice{{Name: "list"}, {Name: "manage"}}
			return false, nil
		}
		if text == "/quit" {
			if u.Turn == "" && !u.Starting() && !u.InputPending() {
				u.Text = ""
				u.Editor.Changed()
				return true, nil
			}
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := settingsCommand(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, u.panes, &u.dirty, u.models, u.modelsLoading, text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Unknown command "+strings.Fields(text)[0]+" · /title, /compact, /clear, /resume, /archive, /btw, /status, /session, /ps, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
			return false, nil
		}
		if text == "" || u.Thread == "" || u.restoring != nil {
			return false, nil
		}
		view := u.attached.presentation.View()
		return false, submitInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case sessionpresentation.QueueComposer:
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace(u.Text)
		if text == "/compact" {
			return false, submitCompact(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.restoring != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, true)
		}
		if u.attached.conversation.ShellMode() || text == "" || strings.HasPrefix(text, "/") || u.Turn == "" && !u.Starting() && !u.InputPending() {
			intent.Operation = sessionpresentation.SubmitComposer
			return u.applyComposerCommand(intent)
		}
		view := u.attached.presentation.View()
		return false, submitInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	}
	return false, nil
}

func applyPromptInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, input terminalsession.PromptInput) (bool, error) {
	if input.Approval.Request != "" {
		return applyApprovalInput(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, notifications, reviewOwner, receipts, observations, input)
	}
	return applyQuestionInput(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, environment, owned, hook, parent, observations, reviewOwner, receipts, input)
}
