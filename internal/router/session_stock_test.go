package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func writeStockRollout(t *testing.T, path string, records ...map[string]any) {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stockRolloutItem(payload any) map[string]any {
	return map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "response_item", "payload": payload}
}

func TestSessionInspectionReadsObservedStockPatchOutcome(t *testing.T) {
	root := t.TempDir()
	replay := filepath.Join(t.TempDir(), "replay")
	store, err := openMekugiReplayStore(replay)
	if err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Add File: observed.txt\n+ready\n*** End Patch\n"
	call := map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": applyPatchToolName, "input": patch, "status": "completed"}
	parent := mekugiHistory{
		ToolName: applyPatchToolName, Script: patch, CarrierName: applyPatchToolName,
		CarrierKind: codeModeCarrierCustom, CarrierPayload: patch, UpstreamItem: map[string]json.RawMessage{
			"type": mustMarshalJSON("custom_tool_call"), "id": mustMarshalJSON("patch-item"),
			"call_id": mustMarshalJSON("patch-call"), "name": mustMarshalJSON(applyPatchToolName),
			"input": mustMarshalJSON(patch), "status": mustMarshalJSON("completed"),
		},
		NativePatches: []nativePatchObservation{{Input: patch, Files: []nativePatchFileSnapshot{{AfterPath: filepath.Join(root, "observed.txt")}}}},
	}
	derived := mekugiHistory{
		ToolName: applyPatchToolName, Script: patch, CarrierName: applyPatchToolName,
		CarrierKind: codeModeCarrierCustom, CarrierPayload: patch,
		CorrelationID: "patch-call\x000", Report: "Success. Added observed.txt.",
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("", filepath.Join(root, "observed.txt"), "", "ready\n")},
	}
	if err := store.put(t.Context(), root, map[string]mekugiHistory{
		"patch-call": parent, nativePatchDerivedCallID("patch-call", 0): derived,
	}); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeStockRollout(t, rollout,
		map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": root}},
		stockRolloutItem(call),
		stockRolloutItem(map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": "Success. Added observed.txt."}),
	)
	var out, diagnostic bytes.Buffer
	status := RunSessionInspection(t.Context(), []string{"--session", rollout, "--replay-dir", replay, "--field", "all", "--ax"}, &out, &diagnostic)
	if status != 0 {
		t.Fatalf("inspection failed: %s", diagnostic.String())
	}
	var result sessionInspection
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 || result.Calls[0].Outcome != "applied" ||
		result.Calls[0].Text["script"].Text != patch || result.Calls[0].Text["report"].Text != derived.Report ||
		result.AX == nil || result.AX.Edits.Calls != 1 || result.AX.Edits.Failed != 0 {
		t.Fatalf("stock inspection = %+v", result)
	}
}

func TestCorpusStockMcatRereadCandidate(t *testing.T) {
	root := t.TempDir()
	rollout := filepath.Join(root, "session.jsonl")
	writeStockRollout(t, rollout,
		map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": root, "source": "cli"}},
		map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "turn_context", "payload": map[string]any{"cwd": root, "model": "gpt-test"}},
		stockRolloutItem(map[string]any{"type": "function_call", "name": nativeExecCommandToolName, "call_id": "first", "arguments": `{"cmd":"mcat file.go 1:100"}`}),
		stockRolloutItem(map[string]any{"type": "function_call_output", "call_id": "first", "output": "read: incomplete; next_call: mread r_123"}),
		stockRolloutItem(map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "second", "input": `await tools.exec_command({cmd: "mcat file.go 50:150"});`}),
	)
	var out, diagnostic bytes.Buffer
	status := RunSessionCorpusInspection(t.Context(), []string{
		"--sessions-dir", root, "--replay-dir", filepath.Join(t.TempDir(), "replay"),
		"--since", "2026-09-14T00:00:00Z", "--until", "2026-09-16T00:00:00Z",
	}, &out, &diagnostic)
	if status != 0 {
		t.Fatalf("corpus inspection failed: %s", diagnostic.String())
	}
	var result sessionCorpus
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || len(result.Sessions[0].Findings) != 1 ||
		result.Sessions[0].Findings[0].Kind != "truncation_reread" || !result.Sessions[0].Findings[0].Candidate ||
		!strings.Contains(out.String(), `"call_id":"second"`) {
		t.Fatalf("mcat corpus result = %+v", result)
	}
}
