package binding

import (
	jsonv1 "encoding/json"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const testCodeModeDescription = "Run JavaScript. All nested tools are available on the global `tools` object, including `tools.exec_command` and `tools.apply_patch`.\n\n### `exec_command`\nRun a command.\n\nexec tool declaration:\n```ts\ndeclare const tools: { exec_command(args: { cmd: string }): Promise<unknown>; };\n```\n\n### `apply_patch`\nApply a patch.\n\nexec tool declaration:\n```ts\ndeclare const tools: { apply_patch(input: string): Promise<unknown>; };\n```"

func testCodeModeAdditionalTools(description string) map[string]any {
	return map[string]any{
		"type": "additional_tools", "role": "developer",
		"tools": []any{
			map[string]any{"type": "namespace", "name": "functions", "tools": []any{
				map[string]any{"type": "custom", "name": "exec", "description": description, "format": map[string]any{"type": "text"}},
				map[string]any{"type": "function", "name": "wait"},
			}},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "send_message"},
			}},
		},
	}
}

func TestStockExecutionNamespacesUseOneInterface(t *testing.T) {
	for _, additional := range []bool{false, true} {
		for _, name := range []string{"exec", "exec_command", "apply_patch"} {
			kind := "custom"
			if name == "exec_command" {
				kind = "function"
			}
			definitions := []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{
				map[string]any{"type": kind, "name": name, "description": testCodeModeDescription},
			}}}
			fields := map[string]jsonv1.RawMessage{"tools": responses.MustMarshalJSON(definitions)}
			if additional {
				fields = map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON([]any{map[string]any{"type": "additional_tools", "role": "developer", "tools": definitions}})}
			}
			owner, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields))
			if name != "exec" {
				if err == nil || !strings.Contains(err.Error(), "required exec interface") {
					t.Fatalf("additional=%v %s accepted: %v", additional, name, err)
				}
				continue
			}
			if err != nil || owner == "" {
				t.Fatalf("additional=%v did not recognize stock exec: %v", additional, err)
			}
			before := responses.MustMarshalJSON(fields)
			if _, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields)); err != nil || !responses.SameJSONValue(before, responses.MustMarshalJSON(fields)) {
				t.Fatalf("recognition changed stock catalog: %v", err)
			}
		}
	}

}

func TestStockExecutionAllowsHostSelectedToolSubset(t *testing.T) {
	for _, tools := range [][]any{
		{map[string]any{"type": "function", "name": "collaboration.wait_agent"}},
	} {
		fields := map[string]jsonv1.RawMessage{"tools": responses.MustMarshalJSON(tools)}
		if _, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields)); err != nil {
			t.Fatalf("stock subset %s: %v", fields["tools"], err)
		}
	}
	description := "Run JS with tools.exec_command and collaboration tools."
	fields := map[string]jsonv1.RawMessage{
		"tools": responses.MustMarshalJSON([]any{}),
		"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(description)}),
	}
	if result, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields)); err != nil || result == "" {
		t.Fatalf("exec subset = %+v, %v", result, err)
	}
	fields = map[string]jsonv1.RawMessage{
		"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools("Run JS with tools.apply_patch.")}),
	}
	if result, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields)); err != nil || result == "" {
		t.Fatalf("patch-only exec subset = %+v, %v", result, err)
	}
}

func TestStockExecutionRecognitionPreservesDirectReset(t *testing.T) {
	for _, additional := range []bool{false, true} {
		for _, namespace := range []bool{false, true} {
			definitions := []any{
				map[string]any{"type": "custom", "name": "exec", "description": testCodeModeDescription},
				map[string]any{"type": "function", "name": "new_context", "description": "Reset with selected sources", "parameters": map[string]any{"type": "object", "properties": map[string]any{"reload": map[string]any{"type": "object"}}}},
				map[string]any{"type": "function", "name": "get_context_remaining"},
			}
			if namespace {
				definitions = []any{map[string]any{"type": "namespace", "name": "functions", "tools": definitions}}
			}
			fields := map[string]jsonv1.RawMessage{"tools": responses.MustMarshalJSON(definitions)}
			if additional {
				fields = map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON([]any{map[string]any{"type": "additional_tools", "role": "developer", "tools": definitions}})}
			}
			before := responses.MustMarshalJSON(fields)
			if owner, err := StockExecutionTool(codexresponses.DecodeToolCatalog(fields)); err != nil || owner != "exec" {
				t.Fatalf("additional=%v namespace=%v: execution=%q error=%v", additional, namespace, owner, err)
			}
			if after := responses.MustMarshalJSON(fields); !responses.SameJSONValue(before, after) {
				t.Fatalf("additional=%v namespace=%v: native catalog changed: before=%s after=%s", additional, namespace, before, after)
			}
		}
	}
}
