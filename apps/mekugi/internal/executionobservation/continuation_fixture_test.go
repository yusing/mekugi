package executionobservation

import (
	"encoding/json"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func continuationTestCatalog() *codexresponses.ToolCatalog {
	return codexresponses.DecodeToolCatalog(map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "functions", "tools": []any{
					map[string]any{"type": "custom", "name": "exec", "description": "### `write_stdin`\nexec tool declaration:\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string }): Promise<unknown>; };\n"},
					map[string]any{"type": "function", "name": "wait", "parameters": map[string]any{
						"type": "object", "properties": map[string]any{"cell_id": map[string]any{"type": "string"}},
					}},
				}},
			}},
		}),
	})
}

func continuationTestCall(name, id, input string) map[string]json.RawMessage {
	field, kind := "input", "custom_tool_call"
	if name == "wait" || name == "write_stdin" || name == "exec_command" {
		field, kind = "arguments", "function_call"
	}
	return map[string]json.RawMessage{
		"type": responses.MustMarshalJSON(kind), "name": responses.MustMarshalJSON(name),
		"call_id": responses.MustMarshalJSON(id), field: responses.MustMarshalJSON(input),
	}
}

func continuationTestOutput(id string, parts ...string) map[string]json.RawMessage {
	var content []any
	for _, part := range parts {
		content = append(content, map[string]any{"type": "input_text", "text": part})
	}
	return map[string]json.RawMessage{
		"type": responses.MustMarshalJSON("custom_tool_call_output"), "call_id": responses.MustMarshalJSON(id),
		"output": responses.MustMarshalJSON(content),
	}
}
