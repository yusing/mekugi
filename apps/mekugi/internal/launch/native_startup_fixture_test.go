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
		launcher := nativeFixtureLauncher{run: func(runtimeContext context.Context, api *sessionapi.Server) error {
			wait, err := startNativeSession(terminalViews, reviewSource, views, conversation, activity, service, prefs, segments, runtimeContext, cmd, stdout, integration.controlObservations, integration.usage, integration.audience, integration.review, integration.execWindows, integration.nativeTrace, integration.retained, integration.changeSource, integration.snapshots, integration.recovered, integration.publishChanges, integration.captureNotice, integration.guard, integration.guards, integration.execTrack, integration.skillsManager, issues, resumeThread, serviceTiers, capture, resumeArgv, debugDirectory, generator, observations, approvals, nil, api, false, nil)
			if err != nil {
				return err
			}
			return wait()
		}}
		hub := sessionapi.NewHub(serverContext, &launcher)
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
	run func(context.Context, *sessionapi.Server) error
}

func (l *nativeFixtureLauncher) Run(ctx context.Context, _ sessionapi.Start, api *sessionapi.Server) error {
	return l.run(ctx, api)
}
