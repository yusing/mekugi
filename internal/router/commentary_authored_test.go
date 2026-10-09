package router

import (
	"encoding/json"
	"testing"
)

func TestFunctionToolCallsPassThrough(t *testing.T) {
	for _, tc := range []struct {
		name, tools, input, namespace, arguments string
	}{
		{"non-strict", `[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`, `[]`, "", ` { "journal" : [{ "op": "log", "text": "Unrecognized stock field" }] } `},
		{"strict", `[{"type":"function","name":"lookup","strict":true,"parameters":{"type":"object"}}]`, `[]`, "", `null`},
		{"owned", `[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"journal":{"type":"boolean"}}}}]`, `[]`, "", ` { "journal" : true } `},
		{"provider", `[]`, `[{"type":"additional_tools","tools":[{"type":"namespace","name":"external","tools":[{"type":"function","name":"lookup","parameters":{"type":"array"}}]}]}]`, "external", `[1, 2]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, proxy, request, workspace := newMekugiTestTransform(t)
			request.fields["tools"] = json.RawMessage(tc.tools)
			var input []any
			if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
				t.Fatal(err)
			}
			input = append(input, testCodeModeAdditionalTools(testCodeModeDescription))
			request.fields["input"] = mustMarshalJSON(input)
			prepared, err := parseResponsesRequest(mustMarshalJSON(request.fields))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := proxy.prepareRequest(t.Context(), &prepared, "passthrough", original.shellThreadID, codexTurnMetadata{
				RequestKind: "turn", ThreadID: original.shellThreadID, Directories: map[string]json.RawMessage{workspace: nil},
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			if !sameJSONValue(prepared.fields["tools"], json.RawMessage(tc.tools)) {
				t.Fatalf("stock schema changed: %s", prepared.fields["tools"])
			}
			call := map[string]any{"type": "function_call", "id": "item", "call_id": "call", "name": "lookup", "namespace": tc.namespace, "arguments": tc.arguments}
			for _, event := range []map[string]any{
				{"type": "response.output_item.added", "item": call},
				{"type": "response.function_call_arguments.delta", "item_id": "item", "delta": tc.arguments},
				{"type": "response.output_item.done", "item": call},
			} {
				raw := mustTestJSON(t, event)
				got, err := transform.TransformSSE(raw)
				if err != nil || len(got) != 1 || string(got[0]) != string(raw) {
					t.Fatalf("SSE passthrough changed: %s, %v", got, err)
				}
			}
			got, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(got, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 1 || jsonString(response.Output[0], "arguments") != tc.arguments {
				t.Fatalf("JSON passthrough changed: %s", got)
			}
			if len(transform.local) != 0 {
				t.Fatal("passthrough calls consumed replay retention")
			}
		})
	}
}
