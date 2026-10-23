package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/commandargs"

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
	SkillsManagerAvailable                   bool
	Args                                     []string
	Catalog                                  []byte
}

func runInvocationFixtureChild() {
	if slices.Contains(os.Args, "--bundled") {
		fmt.Fprintln(os.Stdout, testNativeModelCatalog)
		os.Exit(0)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	session := invocationFixture{BaseURL: os.Getenv("MEKUGI_BASE_URL"), AXReadOutput: os.Getenv(evidence.AXReadOutputEnvironment), Args: os.Args[1:]}
	if startup := os.Getenv("BASH_ENV"); filepath.Base(startup) == "frontend-bash-env" {
		session.FrontendDirectory = filepath.Join(filepath.Dir(startup), "bin")
	}
	for _, arg := range session.Args {
		if arg == "skills.include_instructions=false" {
			session.SkillsManagerAvailable = true
		}
		if raw, ok := strings.CutPrefix(arg, "model_catalog_json="); ok {
			path, err := strconv.Unquote(raw)
			if err != nil {
				os.Exit(91)
			}
			session.Catalog, err = os.ReadFile(path)
			if err != nil {
				os.Exit(92)
			}
		}
	}
	if err := json.MarshalEncode(jsontext.NewEncoder(os.Stdout), &session); err != nil {
		os.Exit(93)
	}
	<-ctx.Done()
	os.Exit(0)
}

func runFixtureInvocation(terminalViews terminalsession.Views, views *sessionpresentation.Service, conversation *conversationmodel.Service, live *previewmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, recovered *recovery.Service, segments *segmentservice.Service, changes *changecapture.Service, usage *accounting.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, diagnosis *diagnostic.Service, ctx context.Context, commands []command.Contribution, projections []binding.InputProjection, guidance *binding.Guidance, naming *binding.SessionNaming, observations *binding.Observation, guards []binding.ShellApproval, options launchoptions.Config, issues *requestexecution.Failures, ready func(invocationFixture), artifacts func([]string)) error {
	settings, err := launchoptions.LoadSettings(options.ThirdParty)
	if err != nil {
		return err
	}
	options = options.ResolveFeatures(settings.Features)
	directory, err := os.MkdirTemp("", "mekugi-invocation-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	executable := filepath.Join(directory, "codex")
	script := "#!/bin/sh\nMEKUGI_INVOCATION_FIXTURE=1; export MEKUGI_INVOCATION_FIXTURE\nexec " + commandargs.Quote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		return err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		writer.Close()
		return err
	}
	defer input.Close()
	observed := make(chan error, 1)
	go func() {
		var session invocationFixture
		err := json.UnmarshalDecode(jsontext.NewDecoder(reader), &session)
		if err == nil && ready != nil {
			ready(session)
		}
		observed <- err
	}()
	code, runErr := runInvocation(terminalViews, views, conversation, live, activity, service, prefs, recovered, segments, changes, usage, agentMetrics, diagnostics, diagnosis, ctx, commands, projections, guidance, naming, observations, guards, options, settings, issues, executable, []string{"exec"}, os.Environ(), input, writer, io.Discard, false, "", nil, false, func() {}, nil, artifacts, nil, "", nil)
	writer.Close()
	observeErr := <-observed
	if observeErr != nil && !errors.Is(observeErr, io.EOF) {
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
