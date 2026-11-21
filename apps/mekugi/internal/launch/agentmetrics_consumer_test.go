package launch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestAgentMetricsSessionInspectionWholeRolloutAndDefectEvidence(t *testing.T) {
	t.Parallel()
	root, replay := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(replay)
	if err != nil {
		t.Fatal(err)
	}
	const patch = "*** Begin Patch\n*** Add File: observed.txt\n+ready\n*** End Patch\n"
	if err := store.put(t.Context(), root, map[string]legacycall.History{
		"edit":                            {ToolName: applyPatchToolName, Script: patch, CarrierName: applyPatchToolName, CarrierKind: codeModeCarrierCustom, CarrierPayload: patch, NativePatches: []legacycall.NativePatch{{Input: patch}}},
		legacycall.PatchCallID("edit", 0): {ToolName: applyPatchToolName, Script: patch, CorrelationID: "edit\x000", Report: "Success", ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(root, "observed.txt"), "", "ready\n")})},
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
		status := inspectioncli.RunSessionInspection(testInspector(new(evidence.Service), new(transportcapture.Service)), testDiagnostics(new(evidence.Service), new(transportcapture.Service)), t.Context(), args, &stdout, &stderr)
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
