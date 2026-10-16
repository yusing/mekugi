package router

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func TestDiagnosticsAbsentNormalSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(evidence.AXReadOutputEnvironment, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := false
	err := runDiagnosticsAbsentSession(t, ctx, []string{"--mode", "passthrough"}, func(session Session) {
		defer cancel()
		ready = true
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		response, err := client.Get(strings.TrimSuffix(session.BaseURL, "/v1") + "/api/metrics")
		if err != nil {
			t.Error(err)
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"schema":"mekugi.capture.metrics.v7"`)) {
			t.Errorf("independent transport metrics: status=%d body=%q err=%v", response.StatusCode, body, err)
		}
	})
	if err != nil || !ready {
		t.Fatalf("normal session ready=%t err=%v", ready, err)
	}
	root, err := debugStorageDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("normal session created debug storage: %v", err)
	}
}

func TestDiagnosticsAbsentRejectsDebugBeforeBundleCreation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(evidence.AXReadOutputEnvironment, "")
	root, err := debugStorageDirectory()
	if err != nil {
		t.Fatal(err)
	}
	err = runDiagnosticsAbsentSession(t, t.Context(), []string{"--debug", "--mode", "passthrough"}, func(Session) {
		t.Error("unavailable diagnostics reached readiness")
	})
	if err == nil || !strings.Contains(err.Error(), "diagnostics are unavailable") {
		t.Fatalf("debug capability error=%v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected debug request created bundle storage: %v", err)
	}
}

func runDiagnosticsAbsentSession(t *testing.T, ctx context.Context, args []string, ready func(Session)) error {
	t.Helper()
	options, err := launchoptions.Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	history := testHistory()
	usage := accounting.New(history)
	if err := usage.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	return RunSession(new(conversationmodel.Service), new(previewmodel.Service), new(activitymodel.Service), history, testPreferences(), testRecovery(), &segmentservice.Service{Reports: testReports()}, testCapture(t, ""), usage,
		new(evidence.Service), new(transportcapture.Service), nil, ctx,
		testCommands(), testInputProjections(), testGuidance(), testNaming(), testGuards(), options, testCriticalErrors(), ready,
		func(paths []string) { t.Errorf("absent diagnostics published artifacts: %v", paths) })
}

func TestDiagnosticsAbsentRejectsOfflineQueriesWithoutMutation(t *testing.T) {
	root := t.TempDir()
	debug := filepath.Join(root, "debug")
	replay := filepath.Join(root, "replay")
	for _, directory := range []string{debug, replay} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Malformed private evidence distinguishes capability rejection from parsing.
	writeDebugInspectionFixture(t, filepath.Join(debug, "router.jsonl"), "private malformed debug evidence")
	writeDebugInspectionFixture(t, filepath.Join(replay, "failure-"+strings.Repeat("a", 64)+".json"), "private malformed failure evidence")
	before := diagnosticsAbsentTree(t, root)
	for _, test := range []struct {
		name string
		args []string
	}{
		{"debug", []string{"--debug-dir", debug}},
		{"failures", []string{"--replay-dir", replay, "--failures"}},
		{"failure-reference", []string{"--replay-dir", replay, "--failures", "failure-reference"}},
		{"missing-debug", []string{"--debug-dir", filepath.Join(root, "missing-debug")}},
		{"missing-replay", []string{"--replay-dir", filepath.Join(root, "missing-replay"), "--failures"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunSessionInspection(testInspector(new(evidence.Service), new(transportcapture.Service)), nil,
				t.Context(), test.args, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "diagnostics are unavailable") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
			}
			if !reflect.DeepEqual(before, diagnosticsAbsentTree(t, root)) {
				t.Fatal("unavailable diagnostics query mutated selected evidence")
			}
		})
	}
}

func diagnosticsAbsentTree(t *testing.T, root string) map[string]struct {
	Mode fs.FileMode
	Time time.Time
	Data string
} {
	t.Helper()
	type entry = struct {
		Mode fs.FileMode
		Time time.Time
		Data string
	}
	result := make(map[string]entry)
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		value := entry{Mode: info.Mode(), Time: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value.Data = string(data)
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
