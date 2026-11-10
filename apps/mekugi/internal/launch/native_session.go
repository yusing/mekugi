package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
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
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// nativeSession owns the serialized execution loop and invocation-lifetime collaborators.
// Helpers borrow explicit view slots and editors, keeping Main's slots independent.
type nativeSession struct {
	preparer     *executionobservation.Preparer
	attached     attachedPresentation
	reviewSource *changecapture.Service
	*composer.Editor
	history  *history.Service
	segments *reports.Service
	sessioncontrol.Client[appServerRequest]
	conversationState     *conversationmodel.Service
	resumePending         []appserver.Message
	clock                 func() time.Time
	backendVersion        string
	title                 string
	titleGenerator        *sessionNaming
	titleUpdates          <-chan sessionTitleUpdate
	side                  *sessioncontrol.Side
	notifications         *nativeNotifications
	questions             nativeQuestionDock
	approvals             nativeApprovalDock
	approvalMode          bool
	reviewHooks           bool
	deduplicateOutput     bool // Launch option; /session distinguishes off from zero.
	guardHookCheck        approvalHookCheck
	statusPanel           *appServerStatusReport
	sessionCapture        *transportcapture.Recorder
	resumePicker          *appServerResumePicker
	statusConfig          appServerStatusConfig
	picker                composerPicker
	skillEnvironment      []string
	commits               *activitypresentation.CommitReader
	commandSegmentWrites  chan commandSegmentWrite
	commandSegmentPending int
	restoredSegments      map[string]*reports.Report
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
	execTrack             *commandsegments.Observer
	shellEdits            appServerShellEdits
	pendingEditReceipts   []*changepresentation.Receipt
	issues                *requestexecution.Failures
	activity              *activitymodel.Service
	observations          *binding.Observation
	retainedSession       history.Session
	ctx                   context.Context
	quitRequested         bool
	archived              bool
	exitUsage             binding.TokenUsage
	resumeArgv            []string
	profile               *launchoptions.Profile
	replayDebugDirectory  string
	serviceTiers          *inferencegateway.Tiers
	models                []appServerModel
	modelsLoading         bool
	ownedImages           map[string]bool
	panes                 *nativePreferences
	restoring             *appServerActivityRestore
	presentationState     *sessionpresentation.Service
	settings              preferences.Applied
	dispatch              func(appserver.Message) error
}

// Freeze the current retained session when a detail read is admitted, even after replacement.
func (session *nativeSession) compactionReader(thread, item string) func(context.Context) (string, error) {
	return compactionSummaryReader(session.retainedSession, session.Client.RolloutPath, thread, item)
}

func (session *nativeSession) overlayOpen(panel *appServerStatusReport, resume *appServerResumePicker) bool {
	return panel != nil || resume != nil
}
