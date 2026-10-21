package launch

import (
	"context"
	"iter"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/scope"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// applyInput binds terminal effects. Each effect settles before the terminal
// resumes interpretation; no application key or pointer switch remains here.
func applyInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, quit *bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, surface *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if attached != nil {
		publishPresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
		publishActivity(attached, terminal)
		defer func() {
			settlePresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
		}()
	}
	for effect := range effects {
		more, err := applyEffect(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, activityService, segments, restoredSegments, files, statusConfig, capture, debugDirectory, quit, dispatch, resumePending, panes, models, modelsLoading, reasoningKey, surface, effect)
		if err != nil || !more {
			return err
		}
		if attached != nil {
			publishPresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
		}
	}
	return nil
}

func applyEffect(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, quit *bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, surface *terminalsession.Attachment, effect terminalsession.InputEffect) (bool, error) {
	switch {
	case effect.Focused != nil:
		if attached != nil && notifications != nil {
			notifications.focused = *effect.Focused
		}
	case effect.Paste != nil:
		_, err := input(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, *effect.Paste)
		return false, err
	case effect.Main != nil:
		shouldQuit, err := applyMainKey(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, *effect.Main)
		(*quit) = (*quit) || shouldQuit
		return false, err
	case effect.Prompt != nil:
		handled, err := applyPromptInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, notifications, *effect.Prompt)
		return !handled, err
	case effect.PendingPrompt != nil:
		if effect.PendingPrompt.Version != attached.presentation.View().Version {
			setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if attached.presentation.DisplayingSide() {
			view := attached.presentation.View()
			_ = attached.presentation.Display(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main})
			*editor = &attached.conversation.Editor
		}
		if !effect.PendingPrompt.ApprovalsFirst || !openApprovals(approvals, questions, attached.conversation, terminal, picker, editor, control.Thread) {
			openQuestions(attached, terminal, picker, editor, questions, approvals, control.Thread)
		}
	case effect.Panel != nil:
		applyPanelAction(attached, terminal, clock, panel, dirty, capture, *effect.Panel)
		(*dirty) = true
	case effect.Resume != nil:
		return false, applyResumeAction(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, approvalMode, quit, *effect.Resume)
	case effect.Menu != nil:
		handled := applyMenuResult(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, side, clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, dirty, parent, environment, *effect.Menu)
		dispatchRewind(attached, terminal, control, picker, clock, parent, (*reviewOwner), shellEdits, retained, (*retainedSession), changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
		return !handled, nil
	case effect.Side != nil:
		return false, applyCloseSide(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, *effect.Side)
	case effect.BTW != nil:
		view := attached.presentation.View()
		if effect.BTW.Version == view.Version && view.BTW != nil && effect.BTW.Exchange == view.BTW.ID {
			return false, closeBTWQuestion(attached, control)
		}
	case effect.Dialog != nil:
		outputIntent(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, environment, parent, files, surface, *effect.Dialog)
	case effect.Reference != nil:
		return referenceInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, owned, environment, parent, *effect.Reference), nil
	case effect.Selection != nil:
		result := effect.Selection
		if len(result.Demands) > 0 {
			source := activitySource(attached, terminal, result.Selection.View)
			if source == nil {
				setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
				return false, nil
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
		} else {
			openSelectionTarget(attached, terminal, clock, activityService, files, surface, result.Target)
		}
	case effect.History != nil:
		if effect.CancelHistory {
			return false, cancelOlderActivity(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, parent, reviewOwner, receipts, notifications, retained, retainedSession, activityService, segments, restoredSegments, dispatch, resumePending, *effect.History)
		}
		return false, loadOlderActivity(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), restoring, side, panel, resumePicker, *effect.History)
	case effect.Display != nil:
		displaySession(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, *effect.Display)
	case effect.Roster != nil:
		intent := effect.Roster
		target := attached.presentation.ActivityTarget(intent.Caller, attached.childSource)
		if displaySession(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, sessionpresentation.DisplayIntent{Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision}, Thread: target}) && target.Caller != "/root" {
			surface.Agents.Selected = target.Caller
		}
	case effect.Details != nil:
		intent := effect.Details
		if intent.Version != attached.presentation.View().Version || activitySource(attached, terminal, intent.View) == nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		switch intent.Kind {
		case terminalsession.ErrorDetails:
			openErrorDetails(attached, terminal, files, surface, intent.View)
		case terminalsession.SkillDetails:
			openSkills(attached, terminal, files, surface, intent.View, intent.Caller)
		}
	case effect.Notice != nil:
		intent := effect.Notice
		view := attached.presentation.View()
		if intent.Version != view.Version || intent.Feedback != view.Status.FeedbackRevision {
			setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false, nil
		}
		if intent.Dismiss {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
			if attached.presentation.Status.Alert {
				attached.presentation.Status.Set("", false)
			}
		} else {
			openBlocks(attached, terminal, files, surface, terminal.Transcript, []activityui.Block{{Kind: "error", Body: intent.Text}})
		}
		(*dirty) = true
	case effect.Clipboard != nil:
		setNotice(&attached.feedback, terminal, presentationTime(clock), effect.Clipboard.Text, effect.Clipboard.Alert)
	case effect.Interrupt != nil:
		return false, interruptSession(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, (*reviewOwner), receipts, *effect.Interrupt)
	case effect.Locked:
		lockNotice(attached, terminal, clock)
	case effect.EditorPath != "":
		return false, &openFileEditor{path: effect.EditorPath}
	}
	return true, nil
}
