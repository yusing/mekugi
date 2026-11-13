package launch

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	segments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// appServerIntegration selects independent collaborators for native-session fixtures.
type appServerIntegration struct {
	preparer            *executionobservation.Preparer
	nativeHub           func(*sessionapi.Hub)
	controlObservations *sessioncontrol.Observations
	bindForks           func(*sessionapi.Server)
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
	workspace, err := filepath.Abs(cmd.Dir)
	if err != nil {
		return nil, err
	}
	if integration.retained.Store == nil {
		directory, err := history.ReplayDirectory()
		if err != nil {
			return nil, err
		}
		integration.retained.Store, err = service.Open(ctx, directory)
		if err != nil {
			return nil, err
		}
		retainedChanges, err := changecapture.New(service, directory)
		if err != nil {
			return nil, err
		}
		integration.changeSource = retainedChanges.Service
	}
	return func() (runErr error) {
		directory, err := os.MkdirTemp("", "native-fixture-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		socket := filepath.Join(directory, "s")
		listener, err := sessionapi.ListenControl(socket)
		if err != nil {
			return err
		}
		defer listener.Close()
		serverContext, stopServer := context.WithCancel(ctx)
		launcher := nativeFixtureLauncher{run: func(runtimeContext context.Context, api *sessionapi.Server, threads *threadExecution) error {
			wait, err := (&nativeSession{preparer: integration.preparer, reviewSource: reviewSource, presentationState: views, conversationState: conversation, activity: activity, history: service, segments: segments, ctx: runtimeContext, controlObservations: integration.controlObservations, usage: integration.usage, audience: integration.audience, review: integration.review, execWindows: integration.execWindows, nativeTrace: integration.nativeTrace, retained: integration.retained, changeSource: integration.changeSource, snapshots: integration.snapshots, recovered: integration.recovered, publishChanges: integration.publishChanges, captureNotice: integration.captureNotice, execTrack: integration.execTrack, issues: issues, serviceTiers: serviceTiers, sessionCapture: capture, resumeArgv: resumeArgv, replayDebugDirectory: debugDirectory, titleGenerator: generator, observations: observations, approvalMode: approvals}).start(prefs, cmd, integration.guard, integration.guards, integration.skillsManager, resumeThread, nil, api, false, threads)
			if err != nil {
				return err
			}
			if integration.bindForks != nil {
				integration.bindForks(api)
			}
			return wait()
		}}
		hub := sessionapi.NewHub(serverContext, &launcher)
		launcher.hub = hub
		if integration.nativeHub != nil {
			integration.nativeHub(hub)
		}
		done := make(chan error, 1)
		go func() { done <- serveSessionHub(serverContext, listener, hub) }()
		defer func() { stopServer(); runErr = errors.Join(runErr, <-done) }()
		transport := sessionapi.ControlTransport(socket)
		defer transport.CloseIdleConnections()
		connection, err := sessionapi.StartNative(ctx, &http.Client{Transport: transport}, "http://mekugi", sessionapi.NativeStart{Workspace: workspace})
		if err != nil {
			return err
		}
		defer connection.Close()
		terminal, _ := terminalViews.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
		terminal.Faint = faint
		client := nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: terminalViews}, editors: make(map[string]*composer.Editor)}
		_, err = client.run(ctx, stdin, stdout)
		if outcome, ok := errors.AsType[*sessionapi.NativeExit](err); ok && outcome.Code == 0 {
			return nil
		}
		return err
	}, nil
}

// Fixtures inject their existing host/owner collaborators while using the real
// socket, runtime admission and native renderer. The fixture owns server teardown.
type nativeFixtureLauncher struct {
	serverLauncher
	run func(context.Context, *sessionapi.Server, *threadExecution) error
}

func (l *nativeFixtureLauncher) Run(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
	threads := &threadExecution{hub: l.hub, setup: &threadSetup{}}
	return l.run(ctx, api, threads)
}

// Real server fixtures use the same admitted owners as production startup.
func newTestServerHub(ctx context.Context) *sessionapi.Hub {
	launcher := &serverLauncher{}
	hub := sessionapi.NewHub(ctx, launcher)
	launcher.hub = hub
	return hub
}
