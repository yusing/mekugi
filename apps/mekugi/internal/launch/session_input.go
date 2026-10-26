package launch

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

func submitInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessionpresentation.SubmitIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	draft, plan, err := attached.presentation.TakeInput(intent, &control.Live, attached.conversation, restoring)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	input := prepareDraft(picker, terminal, attached.cwd, environment, draft)
	attached.conversation.QueueInput(input, plan.Queued)
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side), attached.nativeImages)
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	if !plan.Flush {
		return nil
	}
	terminal.Transcript.Follow()
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
}

func applyComposerCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, intent sessionpresentation.ComposerIntent, preparation *composerPreparation) (bool, error) {
	if err := attached.presentation.AdmitClientComposer(intent); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false, nil
	}
	inputThread := intent.Thread
	if inputThread == "" {
		inputThread = attached.presentation.View().Input.ID
	}
	command := strings.Fields((*editor).Text)
	settingsIntent := intent.Operation == sessionpresentation.IncreaseEffort || intent.Operation == sessionpresentation.DecreaseEffort
	if len(command) > 0 && (intent.Operation == sessionpresentation.SubmitComposer || intent.Operation == sessionpresentation.QueueComposer) {
		settingsIntent = command[0] == "/model" || command[0] == "/effort" || command[0] == "/reasoning" || command[0] == "/tier" || command[0] == "/permissions"
	}
	if settingsIntent && inputThread != attached.presentation.View().Input.ID {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings target changed · retry the control", false)
		return false, nil
	}
	if sideThread := attached.presentation.View().SideThread.ID; sideThread != "" && inputThread == sideThread {
		text := strings.TrimSpace((*editor).Text)
		switch intent.Operation {
		case sessionpresentation.SubmitComposer, sessionpresentation.QueueComposer:
			if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!") {
				return false, submitBTW(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: *model, Effort: *effort, Tier: *tier}, *tiers, *title, *backend, *restoring != nil, side, *panel != nil || *resumePicker != nil, owned, environment, hook, observations, preparation)
			}
			command := strings.Fields(text)
			setting := len(command) > 0 && (command[0] == "/model" || command[0] == "/effort" || command[0] == "/reasoning")
			if text != "/copy" && !setting {
				setNotice(&attached.feedback, terminal, presentationTime(clock), "Return to Main to use this command", false)
				return false, nil
			}
		case sessionpresentation.RestoreWaiting:
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Return to Main to use this control", false)
			return false, nil
		}
	}
	switch intent.Operation {
	case sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort:
		return false, stepReasoning(attached, terminal, control, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, panes, (*restoring) != nil, clock, (*models), (*modelsLoading), intent.Operation == sessionpresentation.IncreaseEffort)
	case sessionpresentation.RestoreWaiting:
		active, err := attached.presentation.ResolveInterrupt(sessionpresentation.InterruptIntent{Version: intent.Version, Target: *intent.Work, Mode: sessioncontrol.RestoreWaiting}, &control.Live, &attached.session)
		if err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false, nil
		}
		if !active {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Original work already ended.", false)
			return false, nil
		}
		editQueued(attached, terminal, control, picker, questions, approvals, clock)
	case sessionpresentation.SubmitComposer:
		if control.Replacement.Archiving() {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace((*editor).Text)
		if text == "/presets" {
			openPresets(parent, attached, control, picker, *editor, preferences.Applied{Model: *model, Effort: *effort, Tier: *tier}, *restoring != nil)
			return false, nil
		}
		if text == "/archive" {
			showArchivePicker(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned))
			return false, nil
		}
		if handled, err := controlsCommand(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, text); handled {
			return false, err
		}
		if titleCommand(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, generator, text) {
			return false, nil
		}
		if sideCommand(text) != "" {
			return false, submitBTW(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, preparation)
		}
		if attached.conversation.ShellMode() {
			return false, submitShell(attached, terminal, control, picker, editor, clock, (*restoring) != nil, environment, parent, (*reviewOwner), shellEdits, retained, (*retainedSession), changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
		}
		if fields := strings.Fields(text); len(fields) > 1 && (fields[0] == "/fork" || fields[0] == "/rewind") {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Use "+fields[0]+" without arguments", true)
			return false, nil
		}
		if text == "/compact" || text == "/clear" || text == "/fork" || text == "/rewind" {
			return false, sessionCommand(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*restoring) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, statusConfig.Provider, approvalMode, text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, resumeCommand(attached, terminal, control, picker, editor, clock, restoring, resumePicker, environment, approvalMode, text)
		}
		if text == "/ps" {
			showRunningProcesses(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, files)
			return false, nil
		}
		if text == "/session" {
			showSessionMetrics(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, capture, debugDirectory)
			return false, nil
		}
		if text == "/status" {
			return false, showStatus(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, (*statusConfig), debugDirectory)
		}
		if text == "/copy" {
			showCopyPicker(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned)
			return false, nil
		}
		if text == "/skills" {
			deleteDraftRange(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))
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
		if handled, err := settingsCommand(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, (*models), (*modelsLoading), text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Unknown command "+strings.Fields(text)[0]+" · Type / to see available commands", true)
			return false, nil
		}
		if text == "" || control.Thread == "" || (*restoring) != nil {
			return false, nil
		}
		view := attached.presentation.View()
		return false, submitInput(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case sessionpresentation.QueueComposer:
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace((*editor).Text)
		if text == "/compact" {
			return false, submitCompact(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*restoring) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, true)
		}
		if attached.conversation.ShellMode() || text == "" || strings.HasPrefix(text, "/") || control.Turn == "" && !control.Starting() && !control.InputPending() {
			intent.Operation = sessionpresentation.SubmitComposer
			return applyComposerCommand(attached, terminal, control, picker, editor, questions, approvals, clock, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, intent, preparation)
		}
		view := attached.presentation.View()
		return false, submitInput(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	}
	return false, nil
}
