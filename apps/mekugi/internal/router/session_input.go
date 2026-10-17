package router

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

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
func applyComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, input terminalsession.ComposerInput) (bool, error) {
	if input.Err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), input.Err.Error(), true)
		return false, nil
	}
	if input.Command.Operation != sessionpresentation.NoComposerOperation {
		return applyComposerCommand(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, input.Command)
	}
	kind, changed := input.LocalEdit, input.Changed
	if input.Editor.Action.Kind != composer.NoAction {
		var err error
		changed, err = attached.presentation.ApplyEditor(input.Editor, attached.conversation)
		if err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
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
		terminal.Main.CursorColumn = nil
	case composer.DeleteAction:
		if !changed {
			return false, nil
		}
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	if kind != composer.UndoAction && kind != composer.RedoAction {
		pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side))
	}
	return false, nil
}

func applyComposerCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, intent sessionpresentation.ComposerIntent) (bool, error) {
	if err := attached.presentation.AdmitComposer(intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false, nil
	}
	switch intent.Operation {
	case sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort:
		return false, stepReasoning(attached, terminal, control, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, panes, dirty, (*restoring) != nil, clock, (*models), (*modelsLoading), reasoningKey, intent.Operation == sessionpresentation.IncreaseEffort)
	case sessionpresentation.RestoreWaiting:
		editQueued(attached, terminal, control, picker, questions, approvals, clock)
	case sessionpresentation.EditExternally:
		if q := questions.Current(); q != nil && q.IsSecret {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "External editor is unavailable for secret answers", false)
			return false, nil
		}
		return false, &composerEditorRequest{intent: intent}
	case sessionpresentation.AttachClipboard:
		pasteImage(attached, terminal, editor, questions, approvals, clock, owned, (*side), parent, intent)
	case sessionpresentation.CancelComposer:
		if control.Replacement.Archiving() {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Archiving session…", false)
			return false, nil
		}
		// As in Codex, Ctrl-C first clears the draft (undoable with Ctrl+Z),
		// then cancels queued compaction, interrupts the active turn, or quits.
		if (*editor).Text != "" {
			deleteDraftRange(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if control.Turn != "" || control.Starting() || control.InputPending() {
				setNotice(&attached.feedback, terminal, presentationTime(clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again interrupts", false)
			}
			if terminal.Main.InterruptLocked {
				setNotice(&attached.feedback, terminal, presentationTime(clock), "Draft cleared · Ctrl+Z restores · Locked · /unlock", false)
			}
			return false, nil
		}
		if cancelQueuedCompact(attached, terminal, control, picker, questions, approvals, clock) {
			return false, nil
		}
		if control.Turn != "" || control.Starting() || control.InputPending() || control.Compaction.Pending() || control.Compaction.ContinuationPending() || len(attached.conversation.Unsent)+len(attached.conversation.Queued) > 0 {
			return false, keyboardInterrupt(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, (*reviewOwner), receipts)
		}
		if terminal.Main.InterruptLocked {
			lockNotice(attached, terminal, clock)
			return false, nil
		}
		return true, nil
	case sessionpresentation.SubmitComposer:
		if control.Replacement.Archiving() {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace((*editor).Text)
		if text == "/archive" {
			showArchivePicker(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned))
			return false, nil
		}
		if handled, err := controlsCommand(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, text); handled {
			return false, err
		}
		if titleCommand(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, generator, dirty, text) {
			return false, nil
		}
		if text == "/btw" || strings.HasPrefix(text, "/btw ") || strings.HasPrefix(text, "/btw\n") || strings.HasPrefix(text, "/btw\t") {
			return false, submitBTW(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment)
		}
		if attached.conversation.ShellMode() {
			return false, submitShell(attached, terminal, control, picker, editor, clock, (*restoring) != nil, environment, parent, (*reviewOwner), shellEdits, retained, (*retainedSession), changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
		}
		if text == "/compact" || text == "/clear" {
			return false, sessionCommand(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*restoring) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, statusConfig.Provider, approvalMode, text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, resumeCommand(attached, terminal, control, picker, editor, clock, restoring, resumePicker, environment, approvalMode, text)
		}
		if text == "/ps" {
			showRunningProcesses(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, files)
			return false, nil
		}
		if text == "/session" {
			showSessionMetrics(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, dirty, capture, debugDirectory)
			return false, nil
		}
		if text == "/status" {
			return false, showStatus(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, dirty, (*statusConfig), debugDirectory)
		}
		if text == "/copy" {
			showCopyPicker(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned)
			return false, nil
		}
		if text == "/skills" {
			deleteDraftRange(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))
			picker.openMenu("menu")
			picker.Choices = []completion.Choice{{Name: "list"}, {Name: "manage"}}
			return false, nil
		}
		if text == "/quit" {
			if control.Turn == "" && !control.Starting() && !control.InputPending() {
				(*editor).Text = ""
				(*editor).Changed()
				return true, nil
			}
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := settingsCommand(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, dirty, (*models), (*modelsLoading), text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Unknown command "+strings.Fields(text)[0]+" · /title, /compact, /clear, /resume, /archive, /btw, /status, /session, /ps, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
			return false, nil
		}
		if text == "" || control.Thread == "" || (*restoring) != nil {
			return false, nil
		}
		view := attached.presentation.View()
		return false, submitInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case sessionpresentation.QueueComposer:
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace((*editor).Text)
		if text == "/compact" {
			return false, submitCompact(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*restoring) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, true)
		}
		if attached.conversation.ShellMode() || text == "" || strings.HasPrefix(text, "/") || control.Turn == "" && !control.Starting() && !control.InputPending() {
			intent.Operation = sessionpresentation.SubmitComposer
			return applyComposerCommand(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, intent)
		}
		view := attached.presentation.View()
		return false, submitInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	}
	return false, nil
}

func applyPromptInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, input terminalsession.PromptInput) (bool, error) {
	if input.Approval.Request != "" {
		return applyApprovalInput(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, notifications, reviewOwner, receipts, observations, input)
	}
	return applyQuestionInput(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, environment, owned, hook, parent, observations, reviewOwner, receipts, input)
}
