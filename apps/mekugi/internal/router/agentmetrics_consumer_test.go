package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	"github.com/yusing/mekugi/libraries/legacycall"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

func TestAgentMetricsReaderEvidenceFailurePreservesExecution(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	for _, phase := range []string{"start", "finish"} {
		for _, missing := range []bool{false, true} {
			name := phase + "/success"
			if missing {
				name = phase + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				path := filepath.Join(directory, "sample.go")
				if !missing {
					writeDebugInspectionFixture(t, path, "package p\nfunc Item() {}\n")
				}
				log := filepath.Join(directory, "reads.jsonl")
				if phase == "start" {
					log = directory // A directory cannot accept an evidence append.
				}
				t.Setenv(evidence.AXReadOutputEnvironment, log)
				t.Setenv("CODEX_THREAD_ID", "reader-failure")
				commands := slices.Clone(testCommands())
				var original command.Output
				calls := 0
				for i := range commands {
					if commands[i].Name != "inspect_file" {
						continue
					}
					run := commands[i].Run
					commands[i].Run = func(ctx context.Context, request command.Request) (command.Output, error) {
						calls++
						out, err := run(ctx, request)
						original = out
						if phase == "finish" {
							// Grow the already-open log sparsely after the real reader runs.
							if err := os.Truncate(log, 64<<20); err != nil {
								t.Fatal(err)
							}
						}
						return out, err
					}
				}
				var stdout, stderr bytes.Buffer
				handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, t.Context(), registry.Frontends["inspect_file"], []string{path}, nil, &stdout, &stderr)
				if !handled || calls != 1 || status != original.ExitCode || stdout.String() != original.Stdout {
					t.Fatalf("handled=%t calls=%d status=%d original=%+v stdout=%q stderr=%q", handled, calls, status, original, stdout.String(), stderr.String())
				}
				if missing && (status == 0 || original.Stderr == "") || !missing && (status != 0 || stdout.Len() == 0) {
					t.Fatalf("reader did not exercise expected outcome: %+v", original)
				}
				diagnostic := "inspect_file: AX read evidence unavailable: "
				if phase == "finish" {
					diagnostic = "inspect_file: AX read evidence incomplete: "
				}
				notice, retained, found := strings.Cut(stderr.String(), "\n")
				if !found || !strings.HasPrefix(notice, diagnostic) || retained != original.Stderr {
					t.Fatalf("instrumentation changed original diagnostic: original=%q stderr=%q", original.Stderr, stderr.String())
				}
			})
		}
	}
}

func TestAgentMetricsReaderAuthenticatedDestinationPrecedesEnvironment(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	log, conflicting := filepath.Join(directory, "manifest.jsonl"), filepath.Join(directory, "environment.jsonl")
	bundle, err := testDiagnostics(new(evidence.Service), nil).OpenBundle(diagnostic.BundleOptions{Root: filepath.Join(directory, "bundles"), ReadLogPath: log, Discover: discoverDebugRollouts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	ctx := context.WithValue(t.Context(), debugContextKey{}, &debugOutput{bundle})
	registry, err := buildToolRegistryForTest(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	writeDebugInspectionFixture(t, filepath.Join(directory, "sample.go"), "package p\nfunc Item() {}\n")
	const thread = "authenticated-metrics-destination"
	invocation := newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID="+thread, evidence.AXReadOutputEnvironment+"="+conflicting)
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil, "inspect_file sample.go", nil, invocation)
	if stdout != "2-2 function Item\n" || stderr != "" || status != 0 {
		t.Fatalf("stdout=%q stderr=%q status=%d", stdout, stderr, status)
	}
	reads, err := new(evidence.Service).ReadReads(t.Context(), log, thread)
	if err != nil || reads.Started != 1 || reads.Completed != 1 || reads.Succeeded != 1 {
		t.Fatalf("authenticated evidence=%+v err=%v", reads, err)
	}
	if _, err := os.Stat(conflicting); !os.IsNotExist(err) {
		t.Fatalf("conflicting environment destination used: %v", err)
	}
}

func TestAgentMetricsSessionInspectionWholeRolloutAndDefectEvidence(t *testing.T) {
	t.Parallel()
	root, replay := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(replay)
	if err != nil {
		t.Fatal(err)
	}
	const patch = "*** Begin Patch\n*** Add File: observed.txt\n+ready\n*** End Patch\n"
	if err := store.put(t.Context(), root, map[string]mekugiHistory{
		"edit":                            {ToolName: applyPatchToolName, Script: patch, CarrierName: applyPatchToolName, CarrierKind: codeModeCarrierCustom, CarrierPayload: patch, NativePatches: []nativePatchObservation{{Input: patch}}},
		legacycall.PatchCallID("edit", 0): {ToolName: applyPatchToolName, Script: patch, CorrelationID: "edit\x000", Report: "Success", ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(root, "observed.txt"), "", "ready\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(root, "rollout.jsonl")
	writeStockRollout(t, rollout,
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": root}},
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		stockRolloutItem(map[string]any{"type": "custom_tool_call", "name": applyPatchToolName, "call_id": "edit", "input": patch}),
		stockRolloutItem(map[string]any{"type": "function_call", "name": nativeExecCommandToolName, "call_id": "read", "arguments": `{"cmd":"true"}`}),
		replayTestItem("thread", "turn", replayTestEpoch+100, replayTestEpoch+350, map[string]any{"type": "CommandExecution", "id": "command", "exit_code": 7}),
		replayTestRecord("event_msg", replayTestEpoch+1000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	)
	artifact := filepath.Join(root, "check.txt")
	const proof = "focused check observed a defect\n"
	writeDebugInspectionFixture(t, artifact, proof)
	assessments := filepath.Join(root, "assessments.json")
	writeDebugInspectionFixture(t, assessments, `[{"call_id":"edit","verdict":"defect","evidence":"check.txt"}]`)
	inspect := func(options ...string) (sessionInspection, int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := append([]string{"--session", rollout, "--replay-dir", replay}, options...)
		status := RunSessionInspection(testInspector(new(evidence.Service), new(transportcapture.Service)), testDiagnostics(new(evidence.Service), new(transportcapture.Service)), t.Context(), args, &stdout, &stderr)
		var result sessionInspection
		if status == 0 {
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		} else if stdout.Len() != 0 {
			t.Fatalf("failed assessment exposed partial report: %s", &stdout)
		}
		return result, status, stderr.String()
	}
	for _, options := range [][]string{{"--limit", "1"}, {"--offset", "1", "--limit", "1"}, {"--call-id", "read"}} {
		result, status, diagnostic := inspect(append(options, "--defects", assessments)...)
		if status != 0 || diagnostic != "" || len(result.Calls) != 1 || result.AX == nil {
			t.Fatalf("inspection=%+v status=%d stderr=%q", result, status, diagnostic)
		}
		commands, completion, defects := result.AX.Commands, result.AX.Completion, result.AX.Defects
		if commands.State != "observed" || commands.Started != 1 || commands.Completed != 1 || commands.DurationMS != 250 || len(commands.Commands) != 1 || commands.Commands[0].ExitCode == nil || *commands.Commands[0].ExitCode != 7 || completion.State != "observed" || completion.CompletedTurns != 1 || completion.DurationMS != 1000 {
			t.Fatalf("decoded metrics depend on pagination: commands=%+v completion=%+v", commands, completion)
		}
		digest := sha256.Sum256([]byte(proof))
		if defects.AssessedCalls != 1 || defects.Reported != 1 || defects.Unassessed != 0 || len(defects.Assessments) != 1 || defects.Assessments[0] != (evidence.AXDefectAssessment{CallID: "edit", Verdict: "defect", Evidence: artifact, SHA256: hex.EncodeToString(digest[:])}) {
			t.Fatalf("assessment lost observed-edit provenance: %+v", defects)
		}
	}
	// A rollout call without an observed edit cannot acquire a defect verdict.
	writeDebugInspectionFixture(t, assessments, `[{"call_id":"read","verdict":"defect","evidence":"check.txt"}]`)
	if _, status, diagnostic := inspect("--defects", assessments); status != 1 || !strings.Contains(diagnostic, "known edit call") {
		t.Fatalf("unobserved edit accepted: status=%d stderr=%q", status, diagnostic)
	}
	writeDebugInspectionFixture(t, assessments, `[{"call_id":"edit","verdict":"defect","evidence":"check.txt"}]`)
	if err := os.Truncate(artifact, (1<<20)+1); err != nil {
		t.Fatal(err)
	}
	if _, status, diagnostic := inspect("--defects", assessments); status != 1 || !strings.Contains(diagnostic, "unavailable") {
		t.Fatalf("oversized assessment evidence accepted: status=%d stderr=%q", status, diagnostic)
	}
}
