package router

import (
	"context"
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/appserver"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// drainKeys consumes queued continuation bytes before deciding that a pending
// Escape was a standalone key. Rendering and tick work can exceed the Escape
// timeout even when the terminal delivered a complete sequence promptly.
func drainKeys(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, quit *bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, keys <-chan byte) error {
	// Bound work so continuous mouse motion or a large paste cannot starve
	// rendering and host events. Exhausting the budget defers Escape expiry.
	for range 256 {
		select {
		case key, ok := <-keys:
			if !ok {
				return io.EOF
			}
			(*dirty) = true
			if event := terminal.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
				if err := applyInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, activityService, segments, restoredSegments, files, statusConfig, capture, debugDirectory, quit, dispatch, resumePending, panes, models, modelsLoading, reasoningKey, terminal, terminal.FrameInput(presentationTime(clock), mekugiVersion()).Dispatch(&terminal.Interaction, event)); err != nil || (*quit) {
					return err
				}
			}
		default:
			if terminal.Decoder.Escape(time.Now()) {
				return applyInput(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, activityService, segments, restoredSegments, files, statusConfig, capture, debugDirectory, quit, dispatch, resumePending, panes, models, modelsLoading, reasoningKey, terminal, terminal.FrameInput(presentationTime(clock), mekugiVersion()).Send(&terminal.Interaction, "\x1b"))
			}
			return nil
		}
	}
	return nil
}

// prepareComposerInput admits terminal-selected prompt suspension before editing.
func prepareComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock) bool {
	ready := terminal.Prompts.Ready()
	publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, presentationTime(clock))
	preparation := terminal.Main.PrepareInput(ready)
	if !preparation.Editable {
		return false
	}
	if intent := preparation.Question; intent != nil {
		if err := attached.presentation.QuestionInput(*intent, questions.Questions); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false
		}
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	}
	if intent := preparation.Approval; intent != nil {
		if err := attached.presentation.ApprovalInput(*intent, approvals.Approvals); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false
		}
		hideApprovals(approvals, terminal, &attached.conversation.PromptEditor, (*editor))
	}
	return true
}

// input consumes complete keys and framed paste events, never terminal byte sequences.
func input(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, reasoningKey **bool, event terminalsession.Input) (bool, error) {
	if event.Kind == terminalsession.NoInput {
		return false, nil
	}
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	switch event.Kind {
	case terminalsession.KeyInput:
		return applyMainKey(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, notifications, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, reasoningKey, terminalsession.KeyEvent(event.Text))
	case terminalsession.PasteStart, terminalsession.PasteInput, terminalsession.PasteEnd:
		if event.Kind == terminalsession.PasteStart {
			terminal.Main.CancelPaste()
			if !prepareComposerInput(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals) {
				return false, nil
			}
			(*editor).Run = composer.NoRun
		}
		if event.Kind != terminalsession.PasteInput {
			publishPresentation(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
		}
		pasted, err := terminal.Main.PasteInput(event, terminal.Prompts)
		if err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		} else if pasted != nil {
			applyPaste(attached, terminal, control, picker, editor, questions, approvals, clock, side, owned, environment, parent, *pasted)
		}
	}
	return false, nil
}
