package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func TestTransportDiagnosticsAbsentDebugSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(evidence.AXReadOutputEnvironment, "")
	options, err := launchoptions.Parse([]string{"--debug", "--mode", "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	history := testHistory()
	usage := accounting.New(history)
	if err := usage.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	var paths []string
	ready := false
	err = RunSession(terminalsession.Views{}, testPresentation(), new(conversationmodel.Service), new(previewmodel.Service), new(activitymodel.Service), history, testPreferences(), testRecovery(), &segmentservice.Service{Reports: testReports()}, testCapture(t, ""), usage, new(evidence.Service), nil, testDiagnostics(new(evidence.Service), nil), ctx, testCommands(), testInputProjections(), testGuidance(), testNaming(), nil, testGuards(), options, nil, func(session Session) {
		defer cancel()
		ready = true
		if session.AXReadOutput == "" {
			t.Error("transport omission disabled independent AX evidence")
		}
		client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		response, err := client.Get(strings.TrimSuffix(session.BaseURL, "/v1") + "/api/metrics")
		if err != nil {
			t.Error(err)
			return
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("absent diagnostics metrics status = %d", response.StatusCode)
		}
	}, func(artifacts []string) { paths = artifacts })
	if err != nil || !ready || len(paths) != 4 {
		t.Fatalf("ready=%t artifacts=%q error=%v", ready, paths, err)
	}
	for i, name := range []string{"router.jsonl", "instructions.jsonl", "reads.jsonl", "ax.json"} {
		info, err := os.Stat(paths[i])
		if err != nil || filepath.Base(paths[i]) != name || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact %q: %v, %v", paths[i], info, err)
		}
	}
	for _, name := range []string{"capture.jsonl", "metrics.json"} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(paths[0]), name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected transport artifact %s: %v", name, err)
		}
	}
	log, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Contains(log, []byte("router_start")) || !bytes.Contains(log, []byte("router_stop")) {
		t.Fatalf("router lifecycle evidence missing: %q, %v", log, err)
	}
	var ax map[string]any
	data, err := os.ReadFile(paths[3])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ax); err != nil || ax["schema"] == nil {
		t.Fatalf("independent AX report missing: %s, %v", data, err)
	}
}

func TestTransportDiagnosticsAbsentRejectsCaptureOutput(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.jsonl")
			if existing {
				writeDebugInspectionFixture(t, path, "retained capture\n")
			}
			options, err := launchoptions.Parse([]string{"--debug", "--mode", "passthrough", "--capture-output", path})
			if err != nil {
				t.Fatal(err)
			}
			// Capability rejection precedes session resources and mutable outputs.
			err = RunSession(terminalsession.Views{}, testPresentation(), new(conversationmodel.Service), new(previewmodel.Service), new(activitymodel.Service), nil, nil, nil, nil, nil, nil, nil, nil, nil, t.Context(), nil, nil, nil, nil, nil, nil, options, nil, func(Session) { t.Error("unavailable capture reached readiness") }, func([]string) { t.Error("unavailable capture created debug artifacts") })
			if err == nil || !strings.Contains(err.Error(), "transport diagnostics are unavailable") {
				t.Fatalf("capture capability error = %v", err)
			}
			data, err := os.ReadFile(path)
			if existing {
				if err != nil || string(data) != "retained capture\n" {
					t.Fatalf("retained capture changed: %q, %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unavailable capture created output: %q, %v", data, err)
			}
		})
	}
}

func TestTransportDiagnosticsAbsentDebugInspection(t *testing.T) {
	directory := debugInspectionFixture(t)
	for _, name := range []string{"capture.jsonl", "metrics.json"} {
		writeDebugInspectionFixture(t, filepath.Join(directory, name), "invalid stale transport evidence")
	}
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionInspection(testInspector(new(evidence.Service), nil), testDiagnostics(new(evidence.Service), nil), t.Context(), []string{"--debug-dir", directory, "--request-id", "req-b", "--field", "all"}, &stdout, &stderr)
	var result diagnostic.Report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || code != 0 || stderr.Len() != 0 {
		t.Fatalf("inspection code=%d stderr=%q decode=%v", code, &stderr, err)
	}
	var names []string
	for _, artifact := range result.Artifacts {
		names = append(names, artifact.Name)
	}
	if !slices.Equal(names, []string{"router.jsonl", "instructions.jsonl", "ax.json", "reads.jsonl"}) || len(result.Metrics) != 0 || len(result.AX) == 0 || result.TotalRequests != 1 || result.TotalEvidence != 2 {
		t.Fatalf("independent debug evidence changed: %+v", result)
	}
	if result.Evidence[0].Source != "router.jsonl" || result.Evidence[0].Text["error"].Text != "original 錯誤 detail" || result.Evidence[1].Source != "instructions.jsonl" || result.Evidence[1].Text["instructions"].Text != "selected instructions" {
		t.Fatalf("router/instruction evidence missing: %+v", result.Evidence)
	}
	for _, name := range []string{"capture.jsonl", "metrics.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != "invalid stale transport evidence" {
			t.Fatalf("stale %s changed: %q, %v", name, data, err)
		}
	}
}

func TestTransportDiagnosticsAbsentOfflineConsumers(t *testing.T) {
	root := t.TempDir()
	rollout := replayTestWrite(t, root, "rollout-root.jsonl", replayTestMeta("root"),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "retained answer"}}}),
		replayTestRecord("response_item", replayTestEpoch+2000, map[string]any{"type": "function_call", "call_id": "read", "name": "shell", "arguments": "{}"}),
	)
	debug := t.TempDir()
	writeDebugInspectionFixture(t, filepath.Join(debug, "capture.jsonl"), "invalid stale capture")
	replayed, err := replayReadTest(t, nil, t.Context(), rollout, debug, 1)
	if err != nil || replayed.Items != 1 || replayed.Providers != 0 {
		t.Fatalf("replay=%+v error=%v", replayed, err)
	}
	for _, event := range replayed.Events {
		if strings.HasPrefix(string(event.Kind), "replay/provider") {
			t.Fatalf("absent transport replayed provider evidence: %+v", event)
		}
	}
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionInspection(testInspector(new(evidence.Service), nil), testDiagnostics(new(evidence.Service), nil), t.Context(), []string{"--session", rollout, "--replay-dir", t.TempDir()}, &stdout, &stderr)
	var single sessionInspection
	if err := json.Unmarshal(stdout.Bytes(), &single); err != nil || code != 0 || stderr.Len() != 0 || single.TotalCalls != 1 || len(single.Calls) != 1 || single.Calls[0].CallID != "read" {
		t.Fatalf("single code=%d result=%+v stderr=%q decode=%v", code, single, &stderr, err)
	}
	stdout.Reset()
	stderr.Reset()
	code = inspectioncli.RunSessionCorpusInspection(testInspector(new(evidence.Service), nil), t.Context(), []string{"--sessions-dir", root, "--replay-dir", t.TempDir(), "--since", "2023-11-14T00:00:00Z", "--until", "2023-11-15T00:00:00Z"}, &stdout, &stderr)
	var corpus sessionCorpus
	if err := json.Unmarshal(stdout.Bytes(), &corpus); err != nil || code != 0 || stderr.Len() != 0 || len(corpus.Unavailable) != 0 || len(corpus.Sessions) != 1 || corpus.Sessions[0].SelectedCalls != 1 || corpus.Sessions[0].ProviderUsage.State != "unavailable" {
		t.Fatalf("corpus code=%d result=%+v stderr=%q decode=%v", code, corpus, &stderr, err)
	}
}

func TestTransportDiagnosticsAbsentRejectsCorpusCaptureBeforeInputs(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionCorpusInspection(testInspector(new(evidence.Service), nil), t.Context(), []string{
		"--sessions-dir", filepath.Join(root, "missing-sessions"), "--capture", filepath.Join(root, "missing-capture"),
	}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "transport diagnostics are unavailable") {
		t.Fatalf("capture capability code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
}
