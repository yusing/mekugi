package router

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestSessionInspectionShellEffects(t *testing.T) {
	for _, tc := range []struct {
		name, status, coverage, want                    string
		files, native, missing, mismatch, shared, patch bool
	}{
		{name: "native completed", native: true, status: execStatusCompleted, coverage: execCoverageExact, files: true, want: "applied"},
		{name: "nested failed with effects", status: execStatusFailed, coverage: execCoveragePartial, files: true, want: "rejected"},
		{name: "nested unconfirmed", coverage: execCoverageExact, files: true, want: "unconfirmed"},
		{name: "partial empty", status: execStatusCompleted, coverage: execCoveragePartial, want: "unconfirmed"},
		{name: "exact empty", status: execStatusCompleted, coverage: execCoverageExact, want: "no_changes"},
		{name: "missing", missing: true, want: "unavailable"},
		{name: "wrong correlation", mismatch: true},
		{name: "shared", shared: true, status: execStatusCompleted, coverage: execCoverageExact, want: "unconfirmed"},
		{name: "patch and shell", patch: true, files: true, status: execStatusCompleted, coverage: execCoveragePartial, want: "applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, replay := t.TempDir(), t.TempDir()
			store, err := openMekugiReplayStore(replay)
			if err != nil {
				t.Fatal(err)
			}
			script := `text(await tools.exec_command({cmd: "printf changed > file"}));`
			name, kind, payload := "exec", "custom_tool_call", "input"
			carrier := codeModeCarrierCustom
			if tc.native {
				name, kind, payload, carrier = nativeExecCommandToolName, "function_call", "arguments", codeModeCarrierFunction
				script = `{"cmd":"printf changed > file"}`
			}
			parent := mekugiHistory{ToolName: name, CarrierName: name, CarrierKind: carrier, CarrierPayload: script, Script: script,
				ExecObservation: &execObservation{CodeMode: !tc.native}}
			derivedID := execDerivedCallID("call", !tc.native)
			derived := mekugiHistory{ToolName: nativeExecCommandToolName, CorrelationID: "call\x00exec", Report: "retained shell result",
				ExecOutcome: &execOutcome{Status: tc.status, Coverage: tc.coverage, CodeMode: !tc.native, ScopeReason: "scope diagnostic"}}
			if tc.status != "" {
				exit := 0
				if tc.status == execStatusFailed {
					exit = 1
				}
				derived.ExecOutcome.Exit = &exit
			}
			if tc.files {
				derived.ChangeID, err = store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), root, "", derived.CorrelationID)
				if err != nil {
					t.Fatal(err)
				}
				derived.ReviewFiles = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", filepath.Join(root, "file"), "", "changed")}
			}
			if tc.mismatch {
				derived.CorrelationID = "other\x00exec"
			}
			if tc.shared {
				derived.ExecOutcome.SharedWith = execDerivedCallID("owner", !tc.native)
			}
			records := map[string]mekugiHistory{}
			if tc.patch {
				parent.NativePatches = []nativePatchObservation{{Input: "patch input"}}
				records[nativePatchDerivedCallID("call", 0)] = mekugiHistory{ToolName: applyPatchToolName, Script: "patch input", CorrelationID: "call\x000", Report: "patch result", ReviewFiles: derived.ReviewFiles}
			}
			records["call"] = parent
			if !tc.missing {
				records[derivedID] = derived
			} else if err := store.put(t.Context(), t.TempDir(), map[string]mekugiHistory{derivedID: derived}); err != nil {
				t.Fatal(err)
			}
			if err := store.put(t.Context(), root, records); err != nil {
				t.Fatal(err)
			}
			rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
			writeStockRollout(t, rollout,
				map[string]any{"type": "session_meta", "payload": map[string]any{"cwd": root}},
				stockRolloutItem(map[string]any{"type": kind, "name": name, "call_id": "call", payload: script}),
			)
			// Snapshot the store after fixture publication. Inspection must not rewrite it.
			entries, err := os.ReadDir(replay)
			if err != nil {
				t.Fatal(err)
			}
			before := make(map[string]os.FileInfo)
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				before[entry.Name()] = info
			}
			var stdout, stderr bytes.Buffer
			code := RunSessionInspection(testHistory(), new(evidence.Service), new(transportcapture.Service), t.Context(), []string{"--session", rollout, "--replay-dir", replay, "--field", "all", "--text-bytes", "15", "--ax"}, &stdout, &stderr)
			if tc.mismatch {
				if code != 1 || !strings.Contains(stderr.String(), "inconsistent effect observation") {
					t.Fatalf("identity accepted: %d %s", code, stderr.String())
				}
				return
			}
			if code != 0 {
				t.Fatalf("inspection: %d %s", code, stderr.String())
			}
			var result sessionInspection
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 {
				t.Fatalf("calls: %+v", result)
			}
			call := result.Calls[0]
			if call.Outcome != tc.want || call.Tool != name || call.CallID != "call" || call.Effects == nil || call.Effects.CallID != derivedID {
				t.Fatalf("call: %+v", call)
			}
			if call.Text["script"].Text != script[:15] || call.Text["script"].Bytes != len(script) {
				t.Fatalf("input changed: %+v", call.Text)
			}
			if tc.missing {
				if call.Effects.Replay != "missing" || call.Effects.ReviewFiles != nil {
					t.Fatalf("missing effects: %+v", call.Effects)
				}
			} else {
				if derived.ExecOutcome.Exit != nil && (call.Effects.Exit == nil || *call.Effects.Exit != *derived.ExecOutcome.Exit) {
					t.Fatalf("exit: %+v", call.Effects)
				}
				if call.Effects.Coverage != tc.coverage || call.Effects.ChangeID != derived.ChangeID || call.Effects.SharedWith != derived.ExecOutcome.SharedWith {
					t.Fatalf("effects: %+v", call.Effects)
				}
				if tc.shared {
					if call.Effects.ReviewFiles != nil {
						t.Fatal("shared effects reported as zero")
					}
				} else if call.Effects.ReviewFiles == nil || *call.Effects.ReviewFiles != len(derived.ReviewFiles) {
					t.Fatalf("file count: %+v", call.Effects)
				}
				report := derived.Report
				if tc.patch {
					report = "patch result\n" + report
				}
				if call.Text["report"].Text != report[:15] || call.Text["report"].OmittedBytes != len(report)-15 || call.Text["diagnostic"].Text != "scope diagnosti" {
					t.Fatalf("text: %+v", call.Text)
				}
			}
			wantEdits := uint64(0)
			if tc.patch {
				wantEdits = 1
			}
			if result.AX.Edits.Calls != wantEdits {
				t.Fatalf("shell changed patch metrics: %+v", result.AX.Edits)
			}
			after, err := os.ReadDir(replay)
			if err != nil || len(after) != len(before) {
				t.Fatalf("store changed: %v", err)
			}
			for _, entry := range after {
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				old := before[entry.Name()]
				if old == nil || old.Mode() != info.Mode() || old.Size() != info.Size() || old.ModTime() != info.ModTime() {
					t.Fatalf("store entry changed: %s", entry.Name())
				}
			}
			if _, err := os.Stat(filepath.Join(root, "file")); !os.IsNotExist(err) {
				t.Fatal("inspection executed retained input")
			}
		})
	}
}
