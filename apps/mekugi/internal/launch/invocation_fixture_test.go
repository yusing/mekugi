package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/commandargs"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"io"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/contracts/command"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

// invocationFixture is the fixture child's observed invocation, published after launch.
type invocationFixture struct {
	BaseURL, AXReadOutput, FrontendDirectory string
}

func runInvocationFixtureChild() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); os.Exit(0) }()
	session := invocationFixture{BaseURL: os.Getenv("MEKUGI_BASE_URL"), AXReadOutput: os.Getenv(evidence.AXReadOutputEnvironment)}
	if startup := os.Getenv("BASH_ENV"); filepath.Base(startup) == "frontend-bash-env" {
		session.FrontendDirectory = filepath.Join(filepath.Dir(startup), "bin")
	}
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			workspace, _ := os.Getwd()
			result["thread"] = map[string]any{"id": "fixture-main", "cwd": workspace}
		case "environment/info":
			if status := os.Getenv("MEKUGI_INVOCATION_FIXTURE_EXIT"); status != "" {
				fmt.Fprintln(os.Stderr, "codex-finished")
				code, _ := strconv.Atoi(status)
				os.Exit(code)
			}
			connection, err := net.Dial("unix", os.Getenv("MEKUGI_INVOCATION_FIXTURE_OBSERVATION"))
			if err != nil {
				os.Exit(92)
			}
			err = json.MarshalWrite(connection, &session)
			connection.Close()
			if err != nil {
				os.Exit(93)
			}
		}
		if err := json.MarshalEncode(encoder, map[string]any{"id": request.ID, "result": result}); err != nil {
			os.Exit(94)
		}
	}
}

func runFixtureInvocation(terminalViews terminalsession.Views, views *sessionpresentation.Service, conversation *conversationmodel.Service, live *previewmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, recovered *recovery.Service, segments *segmentservice.Service, changes *changecapture.Service, usage *accounting.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, diagnosis *diagnostic.Service, ctx context.Context, commands []command.Contribution, projections []binding.InputProjection, guidance *binding.Guidance, naming *binding.SessionNaming, observations *binding.Observation, guards []binding.ShellApproval, options launchoptions.Config, issues *requestexecution.Failures, ready func(invocationFixture), artifacts func([]string)) error {
	if observations == nil {
		bound := conversationbinding.Codex.Observation(conversation)
		observations = &bound
	}
	settings, err := launchoptions.LoadSettings()
	if err != nil {
		return err
	}
	options = options.ResolveFeatures(settings.Features)
	directory, err := os.MkdirTemp("", "mki-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	executable := filepath.Join(directory, "mekugi-app-server")
	script := "#!/bin/sh\nMEKUGI_INVOCATION_FIXTURE=1; export MEKUGI_INVOCATION_FIXTURE\nexec " + commandargs.Quote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		return err
	}
	observationPath := filepath.Join(directory, "ready")
	listener, err := net.Listen("unix", observationPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	observed := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			var session invocationFixture
			err = json.UnmarshalRead(connection, &session)
			if err == nil && ready != nil {
				ready(session)
			}
		}
		observed <- err
	}()
	workspace := directory
	hub := sessionapi.NewHub(ctx, nil)
	defer hub.Close()
	threads := &threadExecution{hub: hub}
	code, runErr := runInvocation(views, conversation, live, activity, service, prefs, recovered, segments, changes, usage, agentMetrics, diagnostics, diagnosis, ctx, commands, projections, guidance, naming, observations, nil, options, settings, issues, executable, nil, append(os.Environ(), "MEKUGI_INVOCATION_FIXTURE_OBSERVATION="+observationPath), io.Discard, "", nil, false, artifacts, sessionapi.NewLive(), workspace, threads)
	listener.Close()
	observeErr := <-observed
	if observeErr != nil && !errors.Is(observeErr, net.ErrClosed) {
		runErr = errors.Join(runErr, observeErr)
	}
	if ctx.Err() != nil && onlyInvocationCancellation(runErr, ctx.Err()) {
		return nil
	}
	if runErr == nil && code != 0 && ctx.Err() == nil {
		return fmt.Errorf("fixture child exited %d", code)
	}
	return runErr
}

// Ignore only the caller's cancellation, preserving any joined cleanup failure.
func onlyInvocationCancellation(err, cause error) bool {
	if err == nil || err == cause {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, part := range joined.Unwrap() {
			if !onlyInvocationCancellation(part, cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyInvocationCancellation(wrapped.Unwrap(), cause)
	}
	return false
}
