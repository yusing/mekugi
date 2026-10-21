package launch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
)

func TestRunSessionReusesParsedConfigWithIndependentDebugOutputs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	options, err := launchoptions.Parse([]string{"--debug", "--ansi-faint", "off"})
	if err != nil {
		t.Fatal(err)
	}
	original := options
	var previousCapture os.FileInfo
	var previousDirectory string
	for run := range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var paths []string
		var readLog string
		// runFixtureInvocation returns only after cancellation has joined its owned work.
		err := runFixtureInvocation(terminalsession.Views{}, testPresentation(), new(conversationmodel.Service), previewmodel.New(changecapture.ReadFile), new(activitymodel.Service), testHistory(), testPreferences(), testRecovery(), &segmentservice.Service{Reports: testReports()}, testCapture(t, ""), newTestUsage(t), new(evidence.Service), new(transportcapture.Service), testDiagnostics(new(evidence.Service), new(transportcapture.Service)), ctx, testCommands(), testInputProjections(), testGuidance(), testNaming(), nil, testGuards(), options, nil, func(session invocationFixture) {
			readLog = session.AXReadOutput
			cancel()
		}, func(artifacts []string) { paths = artifacts })
		cancel()
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if options != original || options.CaptureOutput != "" {
			t.Fatalf("run %d changed the supplied configuration: %+v", run, options)
		}
		if len(paths) != 6 || readLog == "" || readLog != paths[4] {
			t.Fatalf("run %d did not publish its ready debug outputs: reads=%q artifacts=%q", run, readLog, paths)
		}
		directory := filepath.Dir(paths[0])
		if directory == previousDirectory || paths[1] != filepath.Join(directory, "capture.jsonl") {
			t.Fatalf("run %d reused or misdirected generated output: %q", run, paths)
		}
		capture, err := os.Stat(paths[1])
		if err != nil {
			t.Fatal(err)
		}
		if !capture.Mode().IsRegular() || previousCapture != nil && os.SameFile(previousCapture, capture) {
			t.Fatalf("run %d did not create an independent capture file", run)
		}
		previousDirectory, previousCapture = directory, capture
	}
}

func TestRunSessionConfigDebugAndOverride(t *testing.T) {
	for _, cliOff := range []bool{false, true} {
		t.Run(fmt.Sprint(cliOff), func(t *testing.T) {
			for _, key := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "CODEX_HOME", "MEKUGI_RUNTIME_DIR"} {
				t.Setenv(key, t.TempDir())
			}
			path := writeFeatureConfig(t, "debug = true")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--ansi-faint=off"}
			if cliOff {
				args = append(args, "--debug=false")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var paths []string
			err = testRunSession(testHistory(), testRecovery(), ctx, testCommands(), args, nil, func(session invocationFixture) {
				if (session.AXReadOutput != "") == cliOff {
					t.Error("child did not receive effective debug state")
				}
				cancel()
			}, func(artifacts []string) { paths = artifacts })
			if err != nil {
				t.Fatal(err)
			}
			if (len(paths) != 0) == cliOff {
				t.Fatalf("debug artifacts=%v", paths)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("configuration rewritten: %v", err)
			}
		})
	}
}

func TestFeatureConfigFailsBeforeOwnersAndHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("PATH", "")
	writeFeatureConfig(t, "debug = 'credential-secret'")
	code, err := wrapCodex(t.Context(), nil, nil)
	if code != 1 || err == nil || err.Error() != "invalid Mekugi config.toml" {
		t.Fatalf("startup error = %d, %v", code, err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("configuration failure created owner state: %v, %v", entries, err)
	}
}
