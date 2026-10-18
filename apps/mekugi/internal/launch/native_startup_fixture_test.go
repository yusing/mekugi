package launch

import (
	"context"
	"os"
	"os/exec"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	segments "github.com/yusing/mekugi/modules/commandsegments/segments"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// appServerIntegration selects independent collaborators for native-session fixtures.
type appServerIntegration struct {
	controlObservations *sessioncontrol.Observations
	usage               *accounting.Service
	audience            *sessioncontrol.Audience
	review              *sessionreview.Review
	execWindows         *executionobservation.Windows
	nativeTrace         *nativetrace.Reader
	retained            changes.Scope
	changeSource        changes.Service
	snapshots           *changecapture.Workspaces
	recovered           *recovery.Service
	publishChanges      func([]changes.Update)
	captureNotice       func(string, string, string, string)
	guard               *sessioncontrol.ShellApprovals
	guards              []binding.ShellApproval
	execTrack           *segments.Observer
	skillsManager       bool
}

func startAppServerUI(terminalViews terminalsession.Views, reviewSource *changecapture.Service, views *sessionpresentation.Service, conversation *conversationmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, segments *reports.Service, ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, integration appServerIntegration, issues *requestexecution.Failures, resumeThread string, faint bool, serviceTiers *inferencegateway.Tiers, capture *transportcapture.Recorder, resumeArgv []string, debugDirectory string, generator *sessionNaming, observations *binding.Observation, approvals bool) (func() error, error) {
	return startNativeSession(terminalViews, reviewSource, views, conversation, activity, service, prefs, segments, ctx, cmd, stdin, stdout, integration.controlObservations, integration.usage, integration.audience, integration.review, integration.execWindows, integration.nativeTrace, integration.retained, integration.changeSource, integration.snapshots, integration.recovered, integration.publishChanges, integration.captureNotice, integration.guard, integration.guards, integration.execTrack, integration.skillsManager, issues, resumeThread, faint, serviceTiers, capture, resumeArgv, debugDirectory, generator, observations, approvals)
}
