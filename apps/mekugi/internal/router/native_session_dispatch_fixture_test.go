package router

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"iter"

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
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func (u *appServerUI) cancelOlderActivity(intent sessionpresentation.HistoryIntent) error {
	return cancelOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, u.retained, &u.retainedSession, u.activity, u.segments, &u.restoredSegments, u.message, &u.resumePending, intent)
}

func (u *appServerUI) message(m appserver.Message) (err error) {
	return dispatchHostMessage(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.execWindows, u.audience, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, u.activity, &u.streams, u.execTrack, u.nativeTrace, u.usage, &u.exitUsage, u.segments, &u.restoredSegments, u.presentationState, &u.detailFiles, &u.statusConfig, u.issues, &u.quitRequested, u.message, &u.resumePending, &u.commits, u.terminalViews, u.conversationState, u.observations, u.history, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, &u.archived, m)
}

func (u *appServerUI) applyMainKey(next terminalsession.KeyContinuation) (bool, error) {
	return applyMainKey(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, next)
}

func (u *appServerUI) applyInput(surface *terminalsession.Attachment, effects iter.Seq[terminalsession.InputEffect]) error {
	if u == nil {
		return applyInput(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, changes.Scope{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil, "", nil, nil, nil, nil, nil, nil, nil, surface, effects)
	}
	return applyInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, u.activity, u.segments, &u.restoredSegments, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, &u.quitRequested, u.message, &u.resumePending, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, surface, effects)
}

func (u *appServerUI) finishRestoredContent() error {
	return finishRestoredContent(&u.attached, &u.Client, &u.restoring, &u.review, u.message, &u.resumePending)
}

func (u *appServerUI) drainKeys(keys <-chan byte) error {
	return drainKeys(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, u.activity, u.segments, &u.restoredSegments, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, &u.quitRequested, u.message, &u.resumePending, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, keys)
}

func (u *appServerUI) input(event terminalsession.Input) (bool, error) {
	return input(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, u.notifications, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, event)
}

func (u *appServerUI) applyComposerInput(input terminalsession.ComposerInput) (bool, error) {
	return applyComposerInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, &u.model, &u.reasoningEffort, &u.serviceTier, &u.serviceTiers, &u.title, &u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, &u.review, &u.pendingEditReceipts, &u.shellEdits, u.retained, &u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, u.approvalMode, u.titleGenerator, &u.dirty, &u.detailFiles, &u.statusConfig, u.sessionCapture, u.replayDebugDirectory, u.panes, &u.models, &u.modelsLoading, &u.reasoningKey, input)
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
	reasoningKey           *bool
	ownedImages            map[string]bool
	dirty                  bool
	panes                  *nativePreferences
	restoring              *appServerActivityRestore
	presentationState      *sessionpresentation.Service
	childHistoryState      scope.Key[*appServerChildHistory]
}
