package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hpatchSuccessHeader = "Script completed\nWall time 0.1 seconds\nOutput:\n"

func hpatchSuccessFixture(t *testing.T) (*mekugiResponseTransform, mekugiHistory, json.RawMessage) {
	t.Helper()
	transform, overrides := mixedTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform.proxy.replayStore = store
	history, err := transform.translate("success", "new probe.txt\ntype \"probe\\n\"\nshell test \"$(cat probe.txt)\" = probe && printf 'probe passed\\n'", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate: %v %s", err, history.TranslationError)
	}
	if err := transform.proxy.replayStore.put(t.Context(), transform.directory, map[string]mekugiHistory{"success": history}); err != nil {
		t.Fatal(err)
	}
	var result json.RawMessage
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	return transform, history, result
}

func TestHpatchSuccessProjectionAndReplay(t *testing.T) {
	transform, history, result := hpatchSuccessFixture(t)
	recovery := *hpatchRecoveryFor(history)
	raw := mustMarshalJSON(hpatchSuccessHeader + string(result))
	compact, complete := transform.projectHpatchSuccess("success", raw, recovery)
	if !complete || sameJSONValue(compact, raw) {
		t.Fatalf("success not compacted: %s", compact)
	}
	var original struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(result, &original); err != nil {
		t.Fatal(err)
	}
	texts := executionOutputTexts(compact)
	if len(texts) != 3 || texts[0] != hpatchSuccessHeader || texts[1] != jsonString(original.Results[0], "report") {
		t.Fatalf("report/header changed: %s", compact)
	}
	var shell map[string]json.RawMessage
	if err := json.Unmarshal([]byte(texts[2]), &shell); err != nil {
		t.Fatal(err)
	}
	if jsonString(shell, "output") != "probe passed\n" || string(shell["exit_code"]) != "0" {
		t.Fatalf("shell result lost: %s", compact)
	}
	for _, field := range []string{"segment", "line", "kind", "status", "phase", "repair"} {
		if shell[field] != nil {
			t.Fatalf("internal field survived: %s", compact)
		}
		delete(original.Results[1], field)
	}
	if !sameJSONValue(mustMarshalJSON(shell), mustMarshalJSON(original.Results[1])) {
		t.Fatal("native shell fields changed")
	}
	for _, text := range []string{"resume_handle", "expires_at", "segment_completed", "sequence"} {
		if strings.Contains(string(compact), text) {
			t.Fatalf("success overhead %q: %s", text, compact)
		}
	}
	// Later translation metadata writes must preserve the receipt.
	if err := transform.proxy.replayStore.put(t.Context(), transform.directory, map[string]mekugiHistory{"success": history}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(transform.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	replay := *transform
	replay.proxy = &mekugiProxy{replayStore: reopened}
	projection := &mixedOutputProjection{success: replay.projectHpatchSuccess}
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{
		continuationTestOutput("success", texts...),
	})}}
	before := string(request.fields["input"])
	projectExecutionContinuations(projection, &request, continuationTestCatalog(), "exec", map[string]mekugiHistory{"success": history})
	if string(request.fields["input"]) != before {
		t.Fatalf("restart/output-only replay changed compact success: %s", request.fields["input"])
	}
	// The same text on a different call is not an execution receipt.
	if err := reopened.put(t.Context(), transform.directory, map[string]mekugiHistory{"other": history}); err != nil {
		t.Fatal(err)
	}
	if _, accepted := replay.projectHpatchSuccess("other", compact, recovery); accepted {
		t.Fatal("another call borrowed the success receipt")
	}
	var parts []map[string]json.RawMessage
	_ = json.Unmarshal(compact, &parts)
	parts[1]["text"] = mustMarshalJSON("truncated report")
	if _, accepted := replay.projectHpatchSuccess("success", mustMarshalJSON(parts), recovery); accepted {
		t.Fatal("changed output borrowed the success receipt")
	}
}

func TestHpatchSuccessWaitProjection(t *testing.T) {
	transform, history, result := hpatchSuccessFixture(t)
	for _, outputOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "output-only-wrapper"}[outputOnly], func(t *testing.T) {
			items := []map[string]json.RawMessage{continuationTestOutput("success", "Script running with cell ID cell-1\nWall time 0.1 seconds\nOutput:\n")}
			wait := continuationTestCall("wait", "wait", `{"cell_id":"cell-1"}`)
			visible := map[string]mekugiHistory{"success": history}
			if outputOnly {
				visible["wait"] = mekugiHistory{
					ToolName: codeModeCommentaryHistoryTool, CarrierKind: codeModeCarrierCustom,
					Script:       `text(await tools.wait({cell_id:"cell-1"}));`,
					UpstreamItem: continuationTestCall("exec", "wait", `text(await tools.wait({cell_id:"cell-1"}));`),
				}
			} else {
				items = append(items, wait)
			}
			items = append(items, continuationTestOutput("wait", hpatchSuccessHeader, string(result)))
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON(items)}}
			projection := &mixedOutputProjection{recovery: transform.readHpatchRecovery, success: transform.projectHpatchSuccess}
			projectExecutionContinuations(projection, &request, continuationTestCatalog(), "exec", visible)
			first := string(request.fields["input"])
			if strings.Contains(first, "continuation") || strings.Contains(first, "resume_handle") || !strings.Contains(first, "probe passed") {
				t.Fatalf("wait success not compact: %s", first)
			}
			projectExecutionContinuations(projection, &request, continuationTestCatalog(), "exec", visible)
			if string(request.fields["input"]) != first {
				t.Fatalf("wait compaction not idempotent: %s", request.fields["input"])
			}
		})
	}
}

func TestHpatchSuccessCompactionRejectsIncompleteEvidence(t *testing.T) {
	_, history, result := hpatchSuccessFixture(t)
	handle := hpatchRecoveryFor(history).Handle
	for _, change := range []string{"failed", "reconciled", "session", "count", "missing-count", "cleanup", "handle", "exit", "truncated", "terminated"} {
		t.Run(change, func(t *testing.T) {
			var body map[string]json.RawMessage
			_ = json.Unmarshal(result, &body)
			var results []map[string]json.RawMessage
			_ = json.Unmarshal(body["results"], &results)
			var sequence map[string]json.RawMessage
			_ = json.Unmarshal(body["sequence"], &sequence)
			header := hpatchSuccessHeader
			switch change {
			case "failed", "reconciled":
				results[0]["status"] = mustMarshalJSON(change)
			case "session":
				results[1]["session_id"] = mustMarshalJSON(42)
			case "count":
				sequence["started_segments"] = mustMarshalJSON(1)
			case "missing-count":
				delete(sequence, "not_started_segments")
			case "cleanup":
				body["cleanup_diagnostic"] = mustMarshalJSON("unresolved")
			case "handle":
				body["resume_handle"] = mustMarshalJSON("M00000000000000000000000000000000")
			case "exit":
				results[1]["exit_code"] = mustMarshalJSON(1)
			case "terminated":
				header = "Script terminated\nWall time 0.1 seconds\nOutput:\n"
			}
			body["results"], body["sequence"] = mustMarshalJSON(results), mustMarshalJSON(sequence)
			payload := string(mustMarshalJSON(body))
			if change == "truncated" {
				payload = payload[:len(payload)-1]
			}
			if compactHpatchSuccess(mustMarshalJSON(header+payload), handle) != nil {
				t.Fatalf("compacted %s evidence", change)
			}
		})
	}
}

func TestHpatchSuccessPreservesOtherContent(t *testing.T) {
	_, history, result := hpatchSuccessFixture(t)
	image := mustMarshalJSON(map[string]string{"type": "input_image", "image_url": "data:image/png;base64,AAAA"})
	warning := hpatchOutputText("unrelated output warning")
	raw := mustMarshalJSON([]json.RawMessage{hpatchOutputText(hpatchSuccessHeader), hpatchOutputText(string(result)), image, warning})
	compact := compactHpatchSuccess(raw, hpatchRecoveryFor(history).Handle)
	var parts []json.RawMessage
	if json.Unmarshal(compact, &parts) != nil || len(parts) != 5 ||
		!sameJSONValue(parts[3], image) || !sameJSONValue(parts[4], warning) {
		t.Fatalf("other content changed: %s", compact)
	}
}

func TestHpatchSuccessReceiptStorageFailurePreservesFullOutput(t *testing.T) {
	transform, history, result := hpatchSuccessFixture(t)
	raw := mustMarshalJSON(hpatchSuccessHeader + string(result))
	transform.proxy.replayStore.maxBytes = 1
	got, compacted := transform.projectHpatchSuccess("success", raw, *hpatchRecoveryFor(history))
	if compacted || !sameJSONValue(got, raw) {
		t.Fatalf("failed receipt storage exposed compact output: %s", got)
	}
	// No workspace effects are undone by an auxiliary projection failure.
	if data, err := os.ReadFile(filepath.Join(transform.directory, "probe.txt")); err != nil || string(data) != "probe\n" {
		t.Fatalf("projection touched the workspace: %q %v", data, err)
	}
}
