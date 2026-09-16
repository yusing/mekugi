package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGrokWaitWireAlias(t *testing.T) {
	const alias = "_mekugi_b40a44367f3e48917139036e14bc32c2"
	const arguments = `{"cell_id": "125", "yield_time_ms": 1000}`
	for _, additional := range []bool{false, true} {
		name := "catalog"
		if additional {
			name = "additional tools"
		}
		t.Run(name, func(t *testing.T) {
			definitions := []any{
				map[string]any{"type": "function", "name": "wait", "description": "Wait for a running Code Mode cell.",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{"cell_id": map[string]string{"type": "string"}}}},
				// A caller's literal alias-like name must not collide with wait.
				map[string]any{"type": "function", "name": alias},
				map[string]any{"type": "function", "name": "ordinary"},
			}
			input := []any{
				map[string]any{"type": "function_call", "name": "wait", "call_id": "old-call", "arguments": arguments},
				map[string]any{"type": "function_call_output", "call_id": "old-call", "output": "Script running with cell ID 125"},
			}
			request := map[string]any{"model": grokModel, "stream": true, "input": input,
				"tool_choice": map[string]string{"type": "function", "name": "wait"}}
			if additional {
				request["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": definitions}}, input...)
			} else {
				request["tools"] = definitions
			}
			// A new translation has no process-local alias state, as on resume.
			for range 2 {
				tr, err := translateGrokRequest(mustTestJSON(t, request))
				if err != nil {
					t.Fatal(err)
				}
				var wire struct {
					Tools []struct {
						Function struct{ Name string } `json:"function"`
					} `json:"tools"`
					ToolChoice struct {
						Function struct{ Name string } `json:"function"`
					} `json:"tool_choice"`
					Messages []struct {
						ToolCalls []struct {
							ID       string                           `json:"id"`
							Function struct{ Name, Arguments string } `json:"function"`
						} `json:"tool_calls"`
						ToolCallID string `json:"tool_call_id"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(mustMarshalJSON(tr.body), &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Tools[0].Function.Name != alias || wire.Tools[1].Function.Name == alias ||
					wire.Tools[2].Function.Name != "ordinary" || wire.ToolChoice.Function.Name != alias {
					t.Fatalf("inconsistent catalog/choice aliases: %+v", wire)
				}
				call := wire.Messages[0].ToolCalls[0]
				if call.Function.Name != alias || call.Function.Arguments != arguments || call.ID != "old-call" ||
					wire.Messages[1].ToolCallID != "old-call" {
					t.Fatalf("history identity changed: %+v", wire.Messages)
				}
				stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{
					"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
						"index": 0, "id": "next-call", "function": map[string]string{"name": alias, "arguments": arguments},
					}}}, "finish_reason": "tool_calls",
				}}})
				done := 0
				result, err := tr.readGrokStream(strings.NewReader(stream), func(event map[string]any) error {
					if event["type"] == "response.function_call_arguments.done" {
						done++
						if event["name"] != "wait" || event["arguments"] != arguments || event["call_id"] != "next-call" {
							t.Fatalf("aliased call leaked to Codex: %v", event)
						}
					}
					return nil
				})
				if err != nil || done != 1 || result["status"] != "completed" {
					t.Fatalf("wait failed: result=%v done=%d err=%v", result, done, err)
				}
				item := result["output"].([]any)[0].(map[string]any)
				if item["name"] != "wait" || item["arguments"] != arguments || item["call_id"] != "next-call" {
					t.Fatalf("terminal call identity changed: %v", item)
				}
			}
		})
	}
}

func TestGrokWaitIntegerParameters(t *testing.T) {
	parameters := json.RawMessage(`{"type":"object","properties":{"cell_id":{"type":"string"},"yield_time_ms":{"type":"number","description":"Wait time","default":10000},"max_tokens":{"type":"number"},"unrelated":{"type":"number"}},"required":["cell_id"],"additionalProperties":false}`)
	for _, test := range []struct {
		name, namespace, tool string
		wantInteger           bool
	}{
		{"native wait", "", "wait", true},
		{"other function", "", "other", false},
		{"namespaced wait", "other", "wait", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := map[string]any{"model": grokModel, "input": []any{}}
			definition := map[string]any{"type": "function", "name": test.tool, "parameters": parameters}
			if test.namespace != "" {
				request["tools"] = []any{map[string]any{"type": "namespace", "name": test.namespace, "tools": []any{definition}}}
			} else {
				request["tools"] = []any{definition}
			}
			tr, err := translateGrokRequest(mustTestJSON(t, request))
			if err != nil {
				t.Fatal(err)
			}
			raw := tr.body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"]
			var projected map[string]json.RawMessage
			if err := json.Unmarshal(mustMarshalJSON(raw), &projected); err != nil {
				t.Fatal(err)
			}
			var fields map[string]map[string]json.RawMessage
			if err := json.Unmarshal(projected["properties"], &fields); err != nil {
				t.Fatal(err)
			}
			want := "number"
			if test.wantInteger {
				want = "integer"
			}
			for _, name := range []string{"yield_time_ms", "max_tokens"} {
				if jsonString(fields[name], "type") != want {
					t.Fatalf("%s schema not %s: %s", name, want, projected["properties"])
				}
			}
			if jsonString(fields["unrelated"], "type") != "number" ||
				jsonString(fields["yield_time_ms"], "description") != "Wait time" ||
				string(fields["yield_time_ms"]["default"]) != "10000" ||
				string(projected["required"]) != `["cell_id"]` || string(projected["additionalProperties"]) != "false" {
				t.Fatalf("unrelated schema changed: %s", mustMarshalJSON(projected))
			}
			if !strings.Contains(string(parameters), `"yield_time_ms":{"type":"number"`) {
				t.Fatal("original schema was mutated")
			}
		})
	}
	for _, parameters := range []json.RawMessage{
		json.RawMessage(`{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}`),
		json.RawMessage(`{"type":"object","properties":{"cell_id":{"type":"number"},"yield_time_ms":{"type":"number"}}}`),
		json.RawMessage(`{"type":"object","properties":{"cell_id":{"type":"string"},"yield_time_ms":{"type":"integer"}}}`),
	} {
		if got := grokFunctionParameters("", "wait", parameters); string(got) != string(parameters) {
			t.Fatalf("nonmatching/already-correct schema changed: %s", got)
		}
	}
}
