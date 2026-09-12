package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPrepareCommentaryToolsPreservesOwnedSchemas(t *testing.T) {
	fields := map[string]json.RawMessage{
		"tools": mustTestJSON(t, []any{
			map[string]any{
				"type": "function", "name": "lookup", "strict": false,
				"parameters": map[string]any{
					"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}},
					"required": []string{"query"}, "additionalProperties": false,
				},
			},
			map[string]any{
				"type": "function", "name": "strict_lookup", "strict": true,
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			},
			map[string]any{
				"type": "function", "name": "owned_journal", "strict": false,
				"parameters": map[string]any{
					"type": "object", "properties": map[string]any{"journal": map[string]any{"type": "boolean"}},
				},
			},
		}),
		"input": mustTestJSON(t, []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "namespace", "name": "collaboration",
				"tools": []any{map[string]any{
					"type": "function", "name": "followup_task", "strict": false,
					"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
				}},
			}},
		}}),
	}
	additionalTools := bytes.Clone(fields["input"])
	catalog, err := prepareCommentaryTools(fields, decodeResponsesToolCatalog(fields))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog[functionToolKey("", "lookup")]; !ok || len(catalog) != 1 {
		t.Fatalf("commentary catalog = %#v", catalog)
	}
	if !bytes.Equal(fields["input"], additionalTools) {
		t.Fatalf("additional_tools changed:\n got %s\nwant %s", fields["input"], additionalTools)
	}

	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	properties := func(index int) map[string]json.RawMessage {
		t.Helper()
		var parameters, result map[string]json.RawMessage
		if json.Unmarshal(tools[index]["parameters"], &parameters) != nil ||
			json.Unmarshal(parameters["properties"], &result) != nil {
			t.Fatalf("tool %d has malformed parameters", index)
		}
		return result
	}
	if _, ok := properties(0)[commentaryArgumentName]; !ok {
		t.Fatal("extensible tool did not receive commentary")
	}
	if _, ok := properties(1)[commentaryArgumentName]; ok {
		t.Fatal("strict tool schema changed")
	}
	if raw := properties(2)[commentaryArgumentName]; !bytes.Contains(raw, []byte(`"boolean"`)) {
		t.Fatalf("owned commentary changed: %s", raw)
	}
}

func TestPrepareCommentaryToolsPreservesNullTopLevelTools(t *testing.T) {
	fields := map[string]json.RawMessage{"tools": json.RawMessage("null")}
	if _, err := prepareCommentaryTools(fields, decodeResponsesToolCatalog(fields)); err != nil {
		t.Fatal(err)
	}
	if got := string(fields["tools"]); got != "null" {
		t.Fatalf("tools = %s", got)
	}
}

func TestStructuredCommentaryTransformsJSONAndReplay(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "write_stdin"): {
			qualifiedName: "functions.write_stdin",
		},
	}
	originalArguments := `{"session_id":42,"chars":"y","journal":[{"op":"add","text":"Confirming the prompt.","report_now":true}]}`
	payload := mustTestJSON(t, map[string]any{
		"status": "completed", "output": []any{map[string]any{
			"type": "function_call", "id": "item-write", "call_id": "call-write",
			"namespace": "functions", "name": "write_stdin", "arguments": originalArguments,
		}},
	})
	transformed, err := transform.TransformJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if json.Unmarshal(transformed, &response) != nil || len(response.Output) != 2 {
		t.Fatalf("transformed response = %s", transformed)
	}
	if jsonString(response.Output[0], "phase") != "commentary" ||
		!bytes.Contains(response.Output[0]["content"], []byte("Confirming the prompt.")) ||
		jsonString(response.Output[1], "arguments") != `{"chars":"y","session_id":42}` {
		t.Fatalf("transformed output = %s", transformed)
	}
	var commentary struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Content []struct {
			Type        string            `json:"type"`
			Annotations []json.RawMessage `json:"annotations"`
		} `json:"content"`
	}
	if err := json.Unmarshal(mustMarshalJSON(response.Output[0]), &commentary); err != nil {
		t.Fatal(err)
	}
	if commentary.Type != "message" || commentary.Role != "assistant" || commentary.Status != "completed" ||
		len(commentary.Content) != 1 || commentary.Content[0].Type != "output_text" || commentary.Content[0].Annotations == nil {
		t.Fatalf("commentary message shape = %s", mustMarshalJSON(response.Output[0]))
	}

	replay, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{
		response.Output[0], response.Output[1], map[string]any{
			"type": "function_call_output", "call_id": "call-write", "output": `{"ok":true}`,
		},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var replayed []map[string]json.RawMessage
	if json.Unmarshal(replay.fields["input"], &replayed) != nil || len(replayed) != 2 ||
		jsonString(replayed[0], "arguments") != originalArguments {
		t.Fatalf("replayed input = %s", replay.fields["input"])
	}
}

func TestStructuredCommentaryRejectsNonStringValues(t *testing.T) {
	catalog := commentaryToolCatalog{
		functionToolKey("functions", "lookup"): {
			qualifiedName: "functions.lookup",
		},
	}
	for _, value := range []string{"null", "true", "42", `{}`, `[{"unknown":true}]`} {
		item := map[string]json.RawMessage{
			"type": mustMarshalJSON("function_call"), "namespace": mustMarshalJSON("functions"),
			"name": mustMarshalJSON("lookup"), "arguments": mustMarshalJSON(`{"query":"x","journal":` + value + `}`),
		}
		if _, matched, err := extractStructuredCommentary(item, catalog); err == nil || matched {
			t.Fatalf("commentary %s matched = %v, error = %v", value, matched, err)
		}
	}
}

func TestStructuredCommentaryBuffersStreamingArguments(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {
			qualifiedName: "functions.exec_command",
		},
	}
	added := mustTestJSON(t, map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": "item-exec", "call_id": "call-exec",
			"namespace": "functions", "name": "exec_command", "arguments": "",
		},
	})
	if events, err := transform.TransformSSE(added); err != nil || len(events) != 0 {
		t.Fatalf("added events = %q, error %v", events, err)
	}
	arguments := `{"cmd":"go test ./...","journal":[{"op":"add","text":"Testing the project.","report_now":true}]}`
	argumentsDone := mustTestJSON(t, map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "item-exec", "output_index": 0,
		"arguments": arguments,
	})
	if events, err := transform.TransformSSE(argumentsDone); err != nil || len(events) != 1 {
		t.Fatalf("arguments events = %q, error %v", events, err)
	}
	itemDone := mustTestJSON(t, map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": "item-exec", "call_id": "call-exec",
			"namespace": "functions", "name": "exec_command", "arguments": arguments,
		},
	})
	events, err := transform.TransformSSE(itemDone)
	if err != nil || len(events) != 4 {
		t.Fatalf("done events = %q, error %v", events, err)
	}
	if !bytes.Contains(events[0], []byte("Testing the project.")) {
		t.Fatalf("commentary event = %s", events[0])
	}
	for _, event := range events[1:] {
		if bytes.Contains(event, []byte(commentaryArgumentName)) {
			t.Fatalf("router-owned argument leaked: %s", event)
		}
	}
}

func TestBufferedStructuredCommentaryOmitsNullCompletionMessage(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {
			qualifiedName: "functions.exec_command",
		},
	}
	added := mustTestJSON(t, map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": "item-exec", "call_id": "call-exec",
			"namespace": "functions", "name": "exec_command", "arguments": "",
		},
	})
	if events, err := transform.TransformSSE(added); err != nil || len(events) != 0 {
		t.Fatalf("added events = %q, error %v", events, err)
	}
	arguments := `{"cmd":"go test ./...","journal":[{"op":"add","text":"Testing the project.","report_now":true}]}`
	argumentsDone := mustTestJSON(t, map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "item-exec", "output_index": 0,
		"arguments": arguments,
	})
	if events, err := transform.TransformSSE(argumentsDone); err != nil || len(events) != 1 {
		t.Fatalf("arguments events = %q, error %v", events, err)
	}
	itemDone := mustTestJSON(t, map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": "item-exec", "call_id": "call-exec",
			"namespace": "functions", "name": "renamed", "arguments": arguments,
		},
	})
	events, err := transform.TransformSSE(itemDone)
	if err != nil || len(events) != 3 {
		t.Fatalf("done events = %q, error %v", events, err)
	}
	wantTypes := []string{"response.output_item.added", "response.function_call_arguments.done", "response.output_item.done"}
	for index, event := range events {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(event, &envelope) != nil || jsonString(envelope, "type") != wantTypes[index] ||
			bytes.Contains(event, []byte(`"item":null`)) {
			t.Fatalf("event %d = %s", index, event)
		}
	}
}
