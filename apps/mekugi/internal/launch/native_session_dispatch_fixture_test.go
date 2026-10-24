package launch

import (
	"context"
	_ "image/gif"
	_ "image/jpeg"
	"iter"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/contracts/scope"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func (u *appServerUI) message(m appserver.Message) (err error) {
	return dispatchHostMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.execWindows, u.audience, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, u.activity, &u.streams, u.execTrack, u.nativeTrace, u.usage, &u.exitUsage, u.segments, &u.restoredSegments, u.presentationState, &u.detailFiles, &u.statusConfig, u.issues, &u.quitRequested, u.message, &u.resumePending, &u.commits, u.terminalViews, u.conversationState, u.observations, u.history, u.panes, &u.models, &u.modelsLoading, &u.archived, m)
}

func (u *appServerUI) applyMainKey(next terminalsession.KeyContinuation) (bool, error) {
	return applyMainKey(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, next)
}

func (u *appServerUI) applyInput(surface *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if u == nil {
		return applyInput(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, changes.Scope{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, nil, nil, surface, effects)
	}
	return applyInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, u.activity, u.segments, &u.restoredSegments, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, &u.quitRequested, u.message, &u.resumePending, u.panes, &u.models, &u.modelsLoading, surface, effects)
}

func (u *appServerUI) finishRestoredContent() error {
	return finishRestoredContent(&u.attached, &u.Client, &u.picker, &u.restoring, &u.review, u.message, &u.resumePending)
}

func (u *appServerUI) drainKeys(keys <-chan byte) error {
	return drainKeys(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, u.activity, u.segments, &u.restoredSegments, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, &u.quitRequested, u.message, &u.resumePending, u.panes, &u.models, &u.modelsLoading, keys)
}

func (u *appServerUI) input(event terminalsession.Input) (bool, error) {
	return input(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, event)
}

func (u *appServerUI) applyComposerInput(input terminalsession.ComposerInput) (bool, error) {
	return applyComposerInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, input)
}

// appServerUI assembles independently owned collaborators for native-session tests.
type appServerUI struct {
	attached      attachedPresentation
	terminalViews terminalsession.Views
	preparePane   func(*scope.Store)
	reviewSource  *changecapture.Service
	*composer.Editor
	history  *history.Service
	segments *reports.Service
	sessioncontrol.Client[appServerRequest]
	conversationState     *conversationmodel.Service
	resumePending         []appserver.Message
	clock                 func() time.Time // Optional deterministic presentation clock.
	backendVersion        string
	title                 string // Host-confirmed thread name, independent of naming work.
	titleGenerator        *sessionNaming
	titleUpdates          <-chan sessionTitleUpdate
	side                  *sessioncontrol.Side
	notifications         *nativeNotifications
	questions             nativeQuestionDock
	approvals             nativeApprovalDock
	approvalMode          bool // Codex's configured policy applies, not --yolo's.
	guardHookCheck        approvalHookCheck
	statusPanel           *appServerStatusReport
	sessionCapture        *transportcapture.Recorder
	resumePicker          *appServerResumePicker
	statusConfig          appServerStatusConfig
	picker                composerPicker
	skillEnvironment      []string
	commits               *activitypresentation.CommitReader // Attached Activity enrichment owner.
	commandSegmentWrites  chan commandSegmentWrite           // Completed replay writes, consumed only by the UI.
	commandSegmentPending int
	restoredSegments      map[string]*reports.Report // Turn-local, validated and adopted history.
	controlObservations   *sessioncontrol.Observations
	usage                 *accounting.Service
	audience              *sessioncontrol.Audience
	review                *sessionreview.Review
	execWindows           *executionobservation.Windows
	nativeTrace           *nativetrace.Reader
	retained              changes.Scope
	changeSource          changes.Service
	snapshots             *changecapture.Workspaces
	recovered             *recovery.Service
	publishChanges        func([]changes.Update)
	captureNotice         func(string, string, string, string)
	execTrack             *commandsegments.Observer // Per-segment command reports; nil when not tracking.
	shellEdits            appServerShellEdits
	pendingEditReceipts   []*changepresentation.Receipt // Observed edits can precede their host Activity item.
	issues                *requestexecution.Failures
	shell                 *terminalsession.Attachment
	detailFiles
	streams                terminalsession.Streams
	activity               *activitymodel.Service
	observations           *binding.Observation
	retainedSession        history.Session
	ctx                    context.Context
	quitRequested          bool
	archived               bool
	exitUsage              binding.TokenUsage
	resumeArgv             []string
	replayDebugDirectory   string
	model, reasoningEffort string
	serviceTier            string
	serviceTiers           *inferencegateway.Tiers // Invocation defaults and confirmed thread/model choices.
	models                 []appServerModel
	modelsLoading          bool

	ownedImages       map[string]bool
	dirty             bool
	panes             *nativePreferences
	restoring         *appServerActivityRestore
	presentationState *sessionpresentation.Service
}

// applyMainKey settles typed Main effects before resuming terminal interpretation.
func applyMainKey(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, next terminalsession.KeyContinuation) (bool, error) {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	if !prepareComposerInput(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), clock, questions, approvals) {
		return false, nil
	}
	// Acquisition stays application-owned; interpretation consumes its publication.
	if picker.Open && picker.Kind == "" && picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()) != picker.Target {
		picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	}
	publishPresentation(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
	refresh := false
	defer func() {
		if refresh {
			picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
		}
	}()
	focused := terminal == nil || terminal.Focus == 0
	input := terminal.Main.ContinueKey(next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
	for {
		refresh = refresh || input.Regular
		switch {
		case input.Prompt != nil:
			if handled, err := applyPromptInput(attached, terminal, control, picker, editor, questions, approvals, clock, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, side, (*panel) != nil || (*resumePicker) != nil, owned, environment, hook, observations, parent, (*reviewOwner), receipts, notifications, *input.Prompt); handled || err != nil {
				return false, err
			}
		case input.Panel != nil:
			if input.ClearSelection && terminal != nil {
				terminal.Selection = nil
			}
			applyPanelAction(attached, terminal, clock, panel, capture, *input.Panel)
			(*dirty) = true
		case input.Menu != nil:
			handled := applyMenuResult(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, (*title), (*backend), (*restoring) != nil, side, clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), panes, parent, environment, *input.Menu)
			dispatchRewind(attached, terminal, control, picker, clock, parent, (*reviewOwner), shellEdits, retained, (*retainedSession), changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane)
			if handled {
				return false, nil
			}
		case input.Composer != nil:
			return applyComposerInput(attached, terminal, control, picker, editor, questions, approvals, clock, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, dirty, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, *input.Composer)
		}
		if input.Next == nil {
			return false, nil
		}
		publishPresentation(attached, terminal, control, picker, editor, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, (*tiers), (*title), (*backend), (*restoring) != nil, (*side), presentationTime(clock))
		input = terminal.Main.ContinueKey(*input.Next, terminal.Prompts, transcriptView(attached.presentation, terminal), focused)
	}
}

func applyApprovalInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, observations *sessioncontrol.Observations, input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := attached.presentation.ApprovalInput(sessionpresentation.ApprovalInputIntent{Version: input.Version, ApprovalInput: keyInput.Approval}, approvals.Approvals); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return true, nil
	}
	a := approvals.handles[approvals.Pending[0]]
	switch keyInput.Action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		hideApprovals(approvals, terminal, &attached.conversation.PromptEditor, (*editor))
		approvals.AutoOpen = false
	case conversationui.PromptSubmit:
		view := attached.presentation.View()
		return true, decideApproval(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, notifications, reviewOwner, receipts, observations, sessionpresentation.ApprovalIntent{Version: view.Version, Request: a.ID, Choice: a.Choices[a.Selected].ID, Reason: (*editor).Text})
	}
	return true, nil
}

func applyCloseSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, in sessionpresentation.CloseSideIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	if err := attached.presentation.AdmitCloseSide(in); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	return closeSide(attached, terminal, control, editor, questions, approvals, side, owned)
}

// finishPaste inserts a bracketed paste as one edit. A lone path to an image
// file, as terminals paste for copied or dropped files, attaches that file
// instead, followed by a space as in Codex. The user's file is never removed.
func finishPaste(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, text string) {
	if q := questions.Current(); q != nil && !attached.presentation.DisplayingSide() {
		q.Edit(!q.Note)
	}
	path, ok := pastedImagePath(text)
	if !ok {
		if text != "" {
			insertDraft(attached, (*editor), terminal, clock, (*owned), questions, approvals, (*side), text)
			(*editor).Run = composer.NoRun
		}
		return
	}
	insertImage(attached, terminal, editor, questions, approvals, clock, (*owned), (*side), path)
	(*editor).Run = composer.InsertRun // The separating space belongs to the attachment's edit.
	insertDraft(attached, (*editor), terminal, clock, (*owned), questions, approvals, (*side), " ")
	(*editor).Run = composer.NoRun
}

func applyPaste(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, side **sessioncontrol.Side, owned *map[string]bool, environment []string, parent context.Context, paste terminalsession.PasteResult) {
	if err := attached.presentation.AdmitPaste(paste.Intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return
	}
	text := paste.Text
	menu := sessionpresentation.MenuCompletion
	if published := attached.presentation.View().Menu; published != nil {
		menu = published.Kind
	}
	switch {
	case approvals.Open && !attached.presentation.DisplayingSide():
		if a := approvals.calls()[0]; a.DenialChoice() >= 0 && text != "" {
			a.Selected = a.DenialChoice()
			insertDraft(attached, (*editor), terminal, clock, (*owned), questions, approvals, (*side), text)
			(*editor).Run = composer.NoRun
		}
	case menu == sessionpresentation.MenuManageSkills:
		picker.Query += text
	case menu == sessionpresentation.MenuSkills || menu == sessionpresentation.MenuCopy || menu == sessionpresentation.MenuSettings || menu == sessionpresentation.MenuArchive || menu == sessionpresentation.MenuRewind || menu == sessionpresentation.MenuSideReplace:
	default:
		finishPaste(attached, terminal, editor, questions, approvals, clock, side, owned, text)
	}
	picker.Dismissed, picker.Open = picker.completionTarget((*editor).Draft, attached.conversation.ShellMode()), false
	picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
}

// Called only after withRawPane has joined its input reader and restored the
// terminal. The next pane invocation re-enters raw mode and repaints the UI.
// attachImage takes ownership of a clipboard file for cleanup.
func attachImage(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, owned *map[string]bool, side *sessioncontrol.Side, path string) {
	insertImage(attached, terminal, editor, questions, approvals, clock, (*owned), side, path)
	if (*owned) == nil {
		(*owned) = make(map[string]bool)
	}
	(*owned)[path] = true
}

// insertImage is its own undoable edit.
func insertImage(attached *attachedPresentation, terminal *terminalsession.Attachment, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, owned map[string]bool, side *sessioncontrol.Side, path string) {
	(*editor).InsertImage(path)
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages(owned, (*editor), attached.conversation, questions, approvals, terminal.Prompts, side, attached.nativeImages)
}

// Herdr's Codex detector consumes the same OSC 0 activity markers as Codex.
// Source: codex-rs/tui/src/chatwidget/status_surfaces.rs:28:43@1cc7e236
// TERMINAL_TITLE_SPINNER_FRAMES and TERMINAL_TITLE_ACTION_REQUIRED_TEXT.
func (n *nativeNotifications) writeTerminalTitle(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, workspace, main string, questions *prompt.Questions, approvals *prompt.Approvals, busy bool, now time.Time) {
	if n == nil || n.out == nil {
		return
	}
	n.writeTitle(feedback, terminal, clock, workspace, questions.Count(main) > 0 || len(n.blocked) > 0 || len(approvals.Pending) > 0, busy, now)
}

func moveQuestion(questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, editor **composer.Editor, main string, step int) {
	parkQuestion(questions, terminal, *editor, main, questions.Current())
	questions.Move(step)
	*editor = resumeQuestion(questions, conversation, terminal, main, questions.Current())
}

func applyQuestionInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, environment []string, owned *map[string]bool, hook *approvalHookCheck, parent context.Context, observations *sessioncontrol.Observations, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := attached.presentation.QuestionInput(sessionpresentation.QuestionInputIntent{Version: input.Version, QuestionInput: keyInput.Question}, questions.Questions); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return true, nil
	}
	q, c := questions.Current(), questions.activeCall()
	action := keyInput.Action
	switch action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		questions.AutoOpen = false
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	case conversationui.PromptCursor:
		return false, nil
	case conversationui.PromptPrevious:
		moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, -1)
	case conversationui.PromptNext:
		moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, 1)
	case conversationui.PromptSubmit:
		return true, submitQuestions(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, environment, (*owned), hook, parent, observations, reviewOwner, receipts, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID, Incomplete: true})
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		parkQuestion(questions, terminal, (*editor), control.Thread, q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete((*editor).Text, c.Sync, action == conversationui.PromptSkip) {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			terminal.Prompts.Secrets(c.ID).Answers[questions.Index] = answer.Answer
		} else {
			view := attached.presentation.View()
			if !answerQuestion(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, sessionpresentation.AnswerIntent{Version: view.Version,
				Call: c.ID, Question: view.Questions.Call.Questions[questions.Index].ID,
				Choice: q.Selected, Draft: q.Editor.Snapshot.Draft, Skip: action == conversationui.PromptSkip}) {
				return true, nil
			}
		}
		if step, submit := questions.Advance(); step != 0 {
			moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, step)
		} else if submit {
			return true, submitQuestions(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, environment, (*owned), hook, parent, observations, reviewOwner, receipts, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID})
		}
	}
	return true, nil
}

// transcriptView selects display state without changing the input or execution owner.
func transcriptView(presentation *sessionpresentation.Model, terminal *terminalsession.Attachment) *activityui.View {
	view := presentation.View()
	if terminal.Agents != nil && view.Displayed.ID != view.Main.ID {
		terminal.Agents.Conversation = true
		return terminal.Agents
	}
	return terminal.Transcript
}

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func openOutput(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, activity *activitymodel.Service, u *terminalsession.Attachment, view *activityui.View, snippet activityui.Snippet) bool {
	block, ok := view.SnippetBlock(snippet)
	if !ok || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	pages := activitypresentation.DetailPages([]activitypresentation.Value{block.Value})
	if len(block.Members) > 0 {
		pages = nil
		seen := make(map[uint64]bool)
		for _, value := range block.Members {
			member := activityui.Block{Value: value}
			if member.EditSource != "" {
				if seen[member.Source] {
					continue
				}
				if edits, _ := activityEditPages(attached, terminal, activity, view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	openDetails(attached, terminal, files, u, view, pages)
	page := 0
	if block.Output != 0 && (len(block.Members) == 0 || block.Kind == "op") {
		for i, command := range u.Dialog.Pages {
			if command.Source == block.Source && command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.Dialog.ShowPage(page)
	return true
}

func outputIntent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, parent context.Context, files *detailFiles, surface *terminalsession.Attachment, intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		files.closeOutput(surface)
	case terminalsession.DialogCopy:
		copyText(&attached.feedback, terminal, clock, surface, intent.Text)
	case terminalsession.DialogReference:
		if !referenceInput(attached, terminal, control, picker, editor, questions, approvals, clock, settings, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, parent, terminal.Main.Reference("dialog", intent.Text, "")) {
			return
		}
		surface.Focus = 0
		files.closeOutput(surface)
	case terminalsession.DialogOpenLink:
		files.openMarkdownFile(&attached.session, attached.presentation, attached.cwd, activitySource(attached, terminal, surface.Dialog.View), surface, surface.Dialog.View, intent.Text)
	}
}

// applyComposerInput resolves terminal output through the current owners. Every
// semantic action retains the publication and editor target it came from.
func applyComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model *string, effort *string, tier *string, tiers **inferencegateway.Tiers, title *string, backend *string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession *history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), approvalMode bool, generator *sessionNaming, dirty *bool, files *detailFiles, statusConfig *appServerStatusConfig, capture *transportcapture.Recorder, debugDirectory string, panes *nativePreferences, models *[]appServerModel, modelsLoading *bool, input terminalsession.ComposerInput) (bool, error) {
	if input.Err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), input.Err.Error(), true)
		return false, nil
	}
	if input.Command.Operation != sessionpresentation.NoComposerOperation {
		return applyComposerCommand(attached, terminal, control, picker, editor, questions, approvals, clock, model, effort, tier, tiers, title, backend, restoring, side, panel, resumePicker, owned, environment, hook, observations, parent, reviewOwner, receipts, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, approvalMode, generator, files, statusConfig, capture, debugDirectory, panes, models, modelsLoading, input.Command, nil)
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
		pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side), attached.nativeImages)
	}
	return false, nil
}

func applyPromptInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, input terminalsession.PromptInput) (bool, error) {
	if input.Approval.Request != "" {
		return applyApprovalInput(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, notifications, reviewOwner, receipts, observations, input)
	}
	return applyQuestionInput(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, environment, owned, hook, parent, observations, reviewOwner, receipts, input)
}

// displaySession accepts detached targets without consulting terminal state.
func displaySession(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, intent sessionpresentation.DisplayIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	m := attached.presentation
	if intent.Thread != m.View().Main && intent.Thread != m.View().SideThread && attached.session.KnownCaller(intent.Thread.ID) != intent.Thread.Caller {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	crossesSide := m.View().Displayed != intent.Thread && (m.DisplayingSide() || intent.Thread == m.View().SideThread)
	if err := m.Display(intent); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	if crossesSide {
		picker.sideTask = nil
		picker.Open, picker.Kind, picker.Choices = false, "", nil
		picker.cancelScan()
	}
	return true
}

func (u *appServerUI) restoreContent(root binding.ThreadInfo) error {
	return restorePaneContent(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.restoring, &u.statusPanel, &u.resumePicker, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, u.retained, &u.retainedSession, u.activity, u.segments, &u.restoredSegments, u.reviewSource, u.preparePane, root, u.message, &u.resumePending)
}

func registerThread(session *sessionpresentation.Attachment, info binding.ThreadInfo) {
	session.RegisterThread(info.Observation())
	session.Activity().Agent(info.ID).ParentThread = info.ParentThreadID
	restoreThreadSettings(session, info)
}
