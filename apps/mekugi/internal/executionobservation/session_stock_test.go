package executionobservation

import (
	"bytes"

	"cmp"

	"context"

	"encoding/json"

	"os"

	"path/filepath"

	"runtime"

	"strings"

	"testing"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"

	"github.com/yusing/mekugi/libraries/legacycall"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/diffmodel"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestSessionInspectionCanceledEmptySource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeStockRollout(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionInspection(testInspector(nil, nil), testDiagnostics(nil, nil), ctx, []string{"--session", path, "--replay-dir", t.TempDir()}, &stdout, &stderr)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("canceled inspection: code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
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
			"type": responses.MustMarshalJSON("custom_tool_call"), "id": responses.MustMarshalJSON("patch-item"),
			"call_id": responses.MustMarshalJSON("patch-call"), "name": responses.MustMarshalJSON(applyPatchToolName),
			"input": responses.MustMarshalJSON(patch), "status": responses.MustMarshalJSON("completed"),
		},
		NativePatches: []changecapture.PatchObservation{{Input: patch, Files: []changecapture.PatchFile{{AfterPath: filepath.Join(root, "observed.txt")}}}},
	}
	derived := mekugiHistory{
		ToolName: applyPatchToolName, Script: patch, CarrierName: applyPatchToolName,
		CarrierKind: codeModeCarrierCustom, CarrierPayload: patch,
		CorrelationID: "patch-call\x000", Report: "Success. Added observed.txt.",
		ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(root, "observed.txt"), "", "ready\n")},
	}
	if err := store.put(t.Context(), root, map[string]mekugiHistory{
		"patch-call": parent, legacycall.PatchCallID("patch-call", 0): derived,
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
	status := inspectioncli.RunSessionInspection(testInspector(new(evidence.Service), new(transportcapture.Service)), testDiagnostics(new(evidence.Service), new(transportcapture.Service)), t.Context(), []string{"--session", rollout, "--replay-dir", replay, "--field", "all", "--ax"}, &out, &diagnostic)
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
	for _, tc := range []struct {
		name, firstNamespace, secondNamespace, prefix, firstKind, secondKind string
		path                                                                 string
		want                                                                 bool
	}{
		{name: "unqualified", want: true},
		{name: "functions namespace", firstNamespace: "functions", secondNamespace: "functions", want: true},
		{name: "qualified names", prefix: "functions.", want: true},
		{name: "foreign native", firstNamespace: "foreign"},
		{name: "foreign JavaScript", secondNamespace: "foreign"},
		{name: "native wrong kind", firstKind: "custom_tool_call"},
		{name: "JavaScript wrong kind", secondKind: "function_call"},
		{name: "output-only identity", firstKind: "function_call_output"},
		{name: "ambiguous filename", path: "file.go:12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.path != "" {
				if runtime.GOOS == "windows" {
					t.Skip("the unrelated literal-colon filename fixture is unavailable on Windows")
				}
				t.Chdir(t.TempDir())
				if err := os.WriteFile(tc.path, []byte("unrelated content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := cmp.Or(tc.path, "file.go")
			rollout := filepath.Join(root, "session.jsonl")
			writeStockRollout(t, rollout,
				map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": root, "source": "cli"}},
				map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "turn_context", "payload": map[string]any{"cwd": root, "model": "gpt-test"}},
				stockRolloutItem(map[string]any{"type": cmp.Or(tc.firstKind, "function_call"), "name": tc.prefix + nativeExecCommandToolName, "namespace": tc.firstNamespace, "call_id": "first", "arguments": string(responses.MustMarshalJSON(map[string]string{"cmd": "mcat " + path + " 1:100"}))}),
				stockRolloutItem(map[string]any{"type": "function_call_output", "call_id": "first", "output": "read: incomplete; next_call: mread r_123"}),
				stockRolloutItem(map[string]any{"type": cmp.Or(tc.secondKind, "custom_tool_call"), "name": tc.prefix + "exec", "namespace": tc.secondNamespace, "call_id": "second", "input": `await tools.exec_command({cmd: ` + string(responses.MustMarshalJSON("mcat "+path+" 50:150")) + `});`}),
			)
			var out, diagnostic bytes.Buffer
			status := inspectioncli.RunSessionCorpusInspection(testInspector(new(evidence.Service), new(transportcapture.Service)), t.Context(), []string{
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
			if len(result.Sessions) != 1 {
				t.Fatalf("mcat corpus sessions = %+v", result)
			}
			findings := result.Sessions[0].Findings
			if !tc.want {
				if len(findings) != 0 {
					t.Fatalf("unsupported source or identity produced findings: %+v", findings)
				}
				return
			}
			if len(findings) != 1 || findings[0].Kind != "truncation_reread" || !findings[0].Candidate ||
				!strings.Contains(out.String(), `"call_id":"second"`) {
				t.Fatalf("mcat corpus result = %+v", result)
			}
		})
	}
}
