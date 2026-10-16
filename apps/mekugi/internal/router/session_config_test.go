package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	options, err := launchoptions.Parse([]string{"--debug", "--mode", "passthrough", "--ansi-faint", "off"})
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
		// RunSession returns only after cancellation has joined its owned work.
		err := RunSession(new(conversationmodel.Service), new(previewmodel.Service), new(activitymodel.Service), testHistory(), testPreferences(), testRecovery(), &segmentservice.Service{Reports: testReports()}, testCapture(t, ""), newTestUsage(t), new(evidence.Service), new(transportcapture.Service), testDiagnostics(new(evidence.Service), new(transportcapture.Service)), ctx, testCommands(), testInputProjections(), testGuidance(), testNaming(), nil, testGuards(), options, nil, func(session Session) {
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
