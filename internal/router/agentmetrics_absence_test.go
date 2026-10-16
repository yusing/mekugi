package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func TestAgentMetricsAbsentDebugSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	axPath := filepath.Join(t.TempDir(), "configured-reads.jsonl")
	const existing = "retained AX evidence\n"
	writeDebugInspectionFixture(t, axPath, existing)
	t.Setenv(evidence.AXReadOutputEnvironment, axPath)
	options, err := ParseSessionConfig([]string{"--debug", "--mode", "passthrough"})
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
	err = RunSession(history, testRecovery(), &segmentservice.Service{Reports: testReports()}, testChanges(), usage, nil, new(transportcapture.Service),
		ctx, testCommands(), testInputProjections(), testGuidance(), testNaming(), testGuards(), options, nil,
		func(session Session) {
			ready = true
			if session.AXReadOutput != "" {
				t.Errorf("absent metrics enabled AX output: %q", session.AXReadOutput)
			}
			cancel()
		}, func(artifacts []string) { paths = artifacts })
	if err != nil || !ready || len(paths) != 4 {
		t.Fatalf("ready=%t artifacts=%q error=%v", ready, paths, err)
	}
	for i, name := range []string{"router.jsonl", "capture.jsonl", "metrics.json", "instructions.jsonl"} {
		info, err := os.Stat(paths[i])
		if err != nil || filepath.Base(paths[i]) != name || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact %q: %v, %v", paths[i], info, err)
		}
	}
	for _, name := range []string{"reads.jsonl", "ax.json"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(paths[0]), name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected AX artifact %s: %v", name, err)
		}
	}
	if data, err := os.ReadFile(axPath); err != nil || string(data) != existing {
		t.Fatalf("configured AX evidence changed: %q, %v", data, err)
	}
}

func TestAgentMetricsAbsentAuthenticatedReader(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	axPath := filepath.Join(t.TempDir(), "reads.jsonl")
	t.Setenv(evidence.AXReadOutputEnvironment, axPath)
	invocation, err := RecognizeToolWorker(registry.frontends["mcat"])
	if err != nil || invocation == nil {
		t.Fatalf("recognize reader: %v", err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	writeDebugInspectionFixture(t, source, "reader still works\n")
	var stdout, stderr bytes.Buffer
	code := runAuthenticatedToolWorker(testHistory(), testRecovery(), nil, testCommands(), command.HostProcessGroup,
		t.Context(), invocation.directory, invocation.name, []string{source}, nil, &stdout, &stderr)
	if code != 0 || stdout.String() != "reader still works\n" || stderr.Len() != 0 {
		t.Fatalf("reader code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
	if _, err := os.Stat(axPath); !os.IsNotExist(err) {
		t.Fatalf("reader created ambient AX log: %v", err)
	}
}

func TestAgentMetricsAbsentSessionInspection(t *testing.T) {
	root := t.TempDir()
	rollout := filepath.Join(root, "rollout.jsonl")
	writeStockRollout(t, rollout,
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": root}},
		stockRolloutItem(map[string]any{"type": "function_call", "call_id": "read", "name": "shell", "arguments": `{"command":"cat source.txt"}`}),
		stockRolloutItem(map[string]any{"type": "function_call_output", "call_id": "read", "output": "source contents"}),
	)
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(testHistory(), nil, new(transportcapture.Service), t.Context(), []string{"--session", rollout, "--replay-dir", t.TempDir(), "--field", "all"}, &stdout, &stderr)
	var result sessionInspection
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || code != 0 || stderr.Len() != 0 || result.TotalCalls != 1 || len(result.Calls) != 1 || result.Calls[0].CallID != "read" || result.AX != nil {
		t.Fatalf("inspection code=%d result=%+v stderr=%q decode=%v", code, result, &stderr, err)
	}
	for _, option := range [][]string{{"--ax"}, {"--read-log", "unused-read-log"}, {"--defects", "unused-defects"}} {
		stdout.Reset()
		stderr.Reset()
		// A missing rollout distinguishes capability rejection from opening inputs.
		code := RunSessionInspection(testHistory(), nil, new(transportcapture.Service), t.Context(), append([]string{"--session", filepath.Join(root, "missing")}, option...), &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "agent metrics are unavailable") {
			t.Fatalf("option %q: code=%d stdout=%q stderr=%q", option, code, &stdout, &stderr)
		}
	}
}

func TestAgentMetricsAbsentDebugInspection(t *testing.T) {
	directory := debugInspectionFixture(t)
	// Stale AX artifacts must not affect inspection when the module is absent.
	for _, name := range []string{"ax.json", "reads.jsonl"} {
		writeDebugInspectionFixture(t, filepath.Join(directory, name), "invalid stale AX evidence")
	}
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(testHistory(), nil, new(transportcapture.Service), t.Context(), []string{"--debug-dir", directory}, &stdout, &stderr)
	var result debugSessionInspection
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || code != 0 || stderr.Len() != 0 {
		t.Fatalf("inspection code=%d stderr=%q decode=%v", code, &stderr, err)
	}
	var names []string
	for _, artifact := range result.Artifacts {
		names = append(names, artifact.Name)
	}
	if !slices.Equal(names, []string{"router.jsonl", "capture.jsonl", "instructions.jsonl", "metrics.json"}) || len(result.AX) != 0 || result.TotalRequests != 3 || result.TotalEvidence != 3 || len(result.Metrics) == 0 {
		t.Fatalf("non-AX inspection changed: %+v", result)
	}
}

func TestAgentMetricsAbsentIgnoresInvalidCommandTiming(t *testing.T) {
	for _, phase := range []string{"item_started", "item_completed"} {
		t.Run(phase, func(t *testing.T) {
			root, replay := t.TempDir(), t.TempDir()
			rollout := filepath.Join(root, "rollout.jsonl")
			writeStockRollout(t, rollout,
				map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread"}},
				map[string]any{"type": "event_msg", "payload": map[string]any{"type": phase, "started_at_ms": "invalid", "item": map[string]any{"type": "CommandExecution", "id": "command"}}},
				map[string]any{"timestamp": "2026-10-16T00:00:00Z", "type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": "read", "name": "shell", "arguments": "{}"}},
			)
			for _, metrics := range []*evidence.Service{nil, new(evidence.Service)} {
				var stdout, stderr bytes.Buffer
				code := RunSessionInspection(testHistory(), metrics, new(transportcapture.Service), t.Context(), []string{"--session", rollout, "--replay-dir", replay}, &stdout, &stderr)
				if metrics == nil {
					var result sessionInspection
					if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || code != 0 || result.TotalCalls != 1 || len(result.Calls) != 1 || result.Calls[0].CallID != "read" {
						t.Fatalf("absent single inspection: code=%d result=%+v stderr=%q decode=%v", code, result, &stderr, err)
					}
				} else if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "timestamp") {
					t.Fatalf("active timing validation changed: code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
				}
				stdout.Reset()
				stderr.Reset()
				code = RunSessionCorpusInspection(testHistory(), metrics, new(transportcapture.Service), t.Context(), []string{"--sessions-dir", root, "--replay-dir", replay, "--since", "2026-10-15T00:00:00Z", "--until", "2026-10-17T00:00:00Z"}, &stdout, &stderr)
				var corpus sessionCorpus
				if err := json.Unmarshal(stdout.Bytes(), &corpus); err != nil || code != 0 {
					t.Fatalf("corpus: code=%d stderr=%q decode=%v", code, &stderr, err)
				}
				if metrics == nil {
					if len(corpus.Unavailable) != 0 || len(corpus.Sessions) != 1 || corpus.Sessions[0].SelectedCalls != 1 {
						t.Fatalf("absent corpus lost ordinary call: %+v", corpus)
					}
				} else if len(corpus.Sessions) != 0 || len(corpus.Unavailable) != 1 || !strings.Contains(corpus.Unavailable[0].Reason, "timestamp") {
					t.Fatalf("active corpus validation changed: %+v", corpus)
				}
			}
		})
	}
}
