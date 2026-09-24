package router

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCompletedCodeModeCallInputChangeHasSafeDiagnostic(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	transform.local["call"] = mekugiHistory{ToolName: "exec", Script: "original input"}
	_, err := transform.TransformSSE(mustMarshalJSON(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "custom_tool_call", "id": "item", "call_id": "call",
			"name": "exec", "input": "changed private input", "status": "completed"},
	}))
	diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
	if !ok || diagnostic.code != "code_mode_call_input_changed" ||
		strings.Contains(diagnostic.summary, "private input") {
		t.Fatalf("completed call diagnostic = %#v, error = %v", diagnostic, err)
	}
}

func TestCompletedOutputItemCommentaryFailureHasSafeDiagnostic(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {qualifiedName: "functions.exec_command"},
	}
	_, err := transform.TransformSSE(mustMarshalJSON(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "function_call", "id": "item", "call_id": "call",
			"namespace": "functions", "name": "exec_command", "arguments": "private malformed arguments", "status": "completed"},
	}))
	diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
	if !ok || diagnostic.code != "output_item_commentary" ||
		strings.Contains(diagnostic.summary, "private malformed") {
		t.Fatalf("completed commentary diagnostic = %#v, error = %v", diagnostic, err)
	}
}

func TestPrepareStockExecutionPreservesCodeModeAndNativeTools(t *testing.T) {
	guide := newManagedMekugiProxy(t).registry.frontendGuidance
	t.Run("Code Mode", func(t *testing.T) {
		fields := map[string]json.RawMessage{
			"tools": mustMarshalJSON([]any{map[string]any{
				"type": "function", "name": "lookup", "description": "unchanged",
				"parameters": map[string]any{"type": "object"}, "strict": true,
			}}),
			"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(testCodeModeDescription)}),
		}
		var before []map[string]json.RawMessage
		if err := json.Unmarshal(fields["tools"], &before); err != nil {
			t.Fatal(err)
		}
		execution, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide)
		if err != nil || execution.codeMode == nil || execution.native {
			t.Fatalf("execution = %+v, %v", execution, err)
		}
		var after []map[string]json.RawMessage
		if err := json.Unmarshal(fields["tools"], &after); err != nil {
			t.Fatal(err)
		}
		if !sameJSONValue(mustMarshalJSON(before), mustMarshalJSON(after)) {
			t.Fatalf("unrelated stock tool changed: before=%s after=%s", mustMarshalJSON(before), mustMarshalJSON(after))
		}
		if !strings.Contains(string(fields["input"]), "tools.exec_command") ||
			!strings.Contains(string(fields["input"]), "mekugi-journal:start") ||
			!strings.Contains(string(fields["input"]), frontendGuidanceStart) {
			t.Fatalf("Code Mode contract or additive journal guidance missing: %s", fields["input"])
		}
	})

	t.Run("direct", func(t *testing.T) {
		fields := map[string]json.RawMessage{"tools": mustMarshalJSON(testNativeResponsesTools())}
		before := append(json.RawMessage(nil), fields["tools"]...)
		execution, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide)
		if err != nil || execution.codeMode != nil || !execution.native {
			t.Fatalf("execution = %+v, %v", execution, err)
		}
		var original, projected []map[string]json.RawMessage
		if err := json.Unmarshal(before, &original); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fields["tools"], &projected); err != nil {
			t.Fatal(err)
		}
		if !sameJSONValue(mustMarshalJSON(original[1]), mustMarshalJSON(projected[1])) || !strings.Contains(string(projected[0]["description"]), frontendGuidanceStart) {
			t.Fatalf("native projection changed patch tool or omitted frontend guidance: %s", fields["tools"])
		}
	})
}

func TestStockExecutionAllowsHostSelectedToolSubset(t *testing.T) {
	guide := newManagedMekugiProxy(t).registry.frontendGuidance
	for _, tools := range [][]any{
		{map[string]any{"type": "function", "name": nativeExecCommandToolName}},
		{map[string]any{"type": "custom", "name": applyPatchToolName}},
		{map[string]any{"type": "function", "name": "collaboration.wait_agent"}},
	} {
		fields := map[string]json.RawMessage{"tools": mustMarshalJSON(tools)}
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatalf("stock subset %s: %v", fields["tools"], err)
		}
	}
	description := "Run JS with tools.exec_command and collaboration tools."
	fields := map[string]json.RawMessage{
		"tools": mustMarshalJSON([]any{}),
		"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(description)}),
	}
	if result, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil || result.codeMode == nil {
		t.Fatalf("Code Mode subset = %+v, %v", result, err)
	}
	fields = map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{testCodeModeAdditionalTools("Run JS with tools.apply_patch.\n" + guide)}),
	}
	if result, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil || result.codeMode == nil {
		t.Fatalf("patch-only Code Mode subset = %+v, %v", result, err)
	}
	if strings.Contains(string(fields["input"]), "mekugi-frontends:start") || !strings.Contains(string(fields["input"]), "mekugi-journal:start") {
		t.Fatalf("patch-only Code Mode retained irrelevant frontend guide: %s", fields["input"])
	}
}

func TestNativeFrontendGuidanceDeliveredByRequestPreparation(t *testing.T) {
	_, request := newNativeMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) < 2 || strings.Count(jsonString(tools[0], "description"), frontendGuidanceStart) != 1 ||
		!strings.Contains(jsonString(tools[0], "description"), "<tool name=\"mcat\">") ||
		strings.Contains(jsonString(tools[1], "description"), frontendGuidanceStart) {
		t.Fatalf("native frontend guidance not owned by exec_command: %s", request.fields["tools"])
	}
}

func TestCodeModeRejectsOverlappingGuidanceSections(t *testing.T) {
	guide := newManagedMekugiProxy(t).registry.frontendGuidance
	for _, description := range []string{
		"Run JS with tools.exec_command.\n" + frontendGuidanceStart + codeModeJournalStart + codeModeJournalEnd + frontendGuidanceEnd,
		"Run JS with tools.exec_command.\n" + codeModeJournalStart + frontendGuidanceStart + frontendGuidanceEnd + codeModeJournalEnd,
	} {
		fields := map[string]json.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(description)})}
		before := append(json.RawMessage(nil), fields["input"]...)
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err == nil {
			t.Fatalf("accepted overlapping guidance: %q", description)
		}
		if !sameJSONValue(before, fields["input"]) {
			t.Fatalf("rejected guidance changed request: %s", fields["input"])
		}
	}
}

func TestCodeModeStockBatchPassesThroughUnchanged(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	patch := "*** Begin Patch\n*** Add File: batched.txt\n+batched\n*** End Patch\n"
	source := `await Promise.all([tools.apply_patch(` +
		string(mustMarshalJSON(patch)) + `), tools.exec_command({cmd: "printf ready"})]);`
	item := map[string]any{
		"type": "custom_tool_call", "id": "batch-item", "call_id": "batch-call",
		"name": "exec", "input": source, "status": "completed",
	}
	response := mustMarshalJSON(map[string]any{"id": "response", "status": "completed", "output": []any{item}})
	got, err := transform.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Output) != 1 || jsonString(decoded.Output[0], "input") != source {
		t.Fatalf("stock JavaScript changed: %s", got)
	}
	if history, found := transform.local["batch-call"]; !found || len(history.NativePatches) != 1 {
		t.Fatalf("stock batch was not retained before exposure: found=%v history=%+v", found, history)
	}
	history, found, err := transform.proxy.replayStore.lookup(transform.ctx, workspace, "batch-call")
	if err != nil || !found || len(history.NativePatches) != 1 || history.NativePatches[0].Input != patch {
		t.Fatalf("stock batch observation = %+v, found=%v, err=%v", history.NativePatches, found, err)
	}
}

func TestStockExecCommandContinuationUsesWriteStdin(t *testing.T) {
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			continuationTestCall(nativeExecCommandToolName, "run-call", `{"cmd":"sleep 100"}`),
			continuationTestOutput("run-call", "Wall time: 0.1 seconds\nProcess running with session ID 42\nOutput:\npartial"),
		}),
	}}
	projectExecutionContinuations(&request, continuationTestCatalog(), "exec", nil)
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	texts := executionOutputTexts(items[1]["output"])
	if len(texts) != 2 || !strings.Contains(texts[1], "tools.write_stdin") || !strings.Contains(texts[1], `"session_id":42`) {
		t.Fatalf("stock continuation = %v", texts)
	}
}

func TestStockWaitArgumentsPassThrough(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	arguments := `{"cell_id":"cell-1","yield_time_ms":1}`
	call := map[string]any{
		"type": "function_call", "id": "wait-item", "call_id": "wait-call",
		"namespace": "functions", "name": "wait", "arguments": arguments,
	}
	response := mustMarshalJSON(map[string]any{"status": "completed", "output": []any{call}})
	visible, err := transform.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &decoded); err != nil || len(decoded.Output) != 1 ||
		jsonString(decoded.Output[0], "arguments") != arguments {
		t.Fatalf("stock wait arguments changed: %s, %v", visible, err)
	}
	for _, event := range []map[string]any{
		{"type": "response.output_item.added", "item": call},
		{"type": "response.output_item.done", "item": call},
	} {
		wire := mustMarshalJSON(event)
		stream, err := transform.TransformSSE(wire)
		if err != nil || len(stream) != 1 || string(stream[0]) != string(wire) {
			t.Fatalf("stock streaming wait changed: %s, %v", stream, err)
		}
	}
}

func TestCodeModeWriteStdinArgumentsPassThrough(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t)
	source := `text(await tools.write_stdin({session_id:42,chars:"",yield_time_ms:1}));`
	call := map[string]any{
		"type": "custom_tool_call", "id": "code-wait-item", "call_id": "code-wait-call",
		"name": "exec", "input": source, "status": "completed",
	}
	visible, err := transform.TransformJSON(mustMarshalJSON(map[string]any{"status": "completed", "output": []any{call}}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(visible, &decoded); err != nil || len(decoded.Output) != 1 ||
		jsonString(decoded.Output[0], "input") != source {
		t.Fatalf("Code Mode write_stdin changed: %s, %v", visible, err)
	}
}
