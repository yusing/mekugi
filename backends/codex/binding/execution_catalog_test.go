package binding

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const catalogTestGuide = "<frontend-guide>reader commands</frontend-guide>"

func catalogTestGuidance() *Guidance {
	return &Guidance{BaseInstructions: func(raw jsonv1.RawMessage, _ string, _ bool) (jsonv1.RawMessage, error) { return raw, nil }, ToolDescription: func(description, guide string, remove bool) (string, error) {
		description = strings.ReplaceAll(description, catalogTestGuide, "")
		if !remove {
			description += guide
		}
		return description, nil
	}}
}

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

func TestPrepareStockExecutionPreservesCodeModeAndNativeTools(t *testing.T) {
	guide := FrontendGuidance{Descriptions: catalogTestGuide}
	t.Run("exec", func(t *testing.T) {
		fields := map[string]jsonv1.RawMessage{
			"tools": responses.MustMarshalJSON([]any{map[string]any{
				"type": "function", "name": "lookup", "description": "unchanged",
				"parameters": map[string]any{"type": "object"}, "strict": true,
			}}),
			"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(testCodeModeDescription)}),
		}
		var before []map[string]jsonv1.RawMessage
		if err := json.Unmarshal(fields["tools"], &before); err != nil {
			t.Fatal(err)
		}
		execution, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance())
		if err != nil || execution == "" {
			t.Fatalf("execution = %+v, %v", execution, err)
		}
		var after []map[string]jsonv1.RawMessage
		if err := json.Unmarshal(fields["tools"], &after); err != nil {
			t.Fatal(err)
		}
		if !responses.SameJSONValue(responses.MustMarshalJSON(before), responses.MustMarshalJSON(after)) {
			t.Fatalf("unrelated stock tool changed: before=%s after=%s", responses.MustMarshalJSON(before), responses.MustMarshalJSON(after))
		}
		if !strings.Contains(string(fields["input"]), catalogTestGuide) ||
			!strings.Contains(string(fields["input"]), "reader commands") {
			t.Fatalf("exec contract or frontend guidance missing: %s", fields["input"])
		}
	})

	t.Run("unsupported execution", func(t *testing.T) {
		fields := map[string]jsonv1.RawMessage{"tools": responses.MustMarshalJSON([]any{map[string]any{"type": "function", "name": "exec_command"}})}
		before := responses.MustMarshalJSON(fields)
		if _, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance()); err == nil || !strings.Contains(err.Error(), "required exec interface") {
			t.Fatalf("unsupported model accepted: %v", err)
		}
		if !responses.SameJSONValue(before, responses.MustMarshalJSON(fields)) {
			t.Fatal("rejected catalog was changed")
		}
	})

}

func TestStockExecutionNamespacesUseOneInterface(t *testing.T) {
	guide := FrontendGuidance{Descriptions: catalogTestGuide}
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
			owner, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance())
			if name != "exec" {
				if err == nil || !strings.Contains(err.Error(), "required exec interface") {
					t.Fatalf("additional=%v %s accepted: %v", additional, name, err)
				}
				continue
			}
			if err != nil || owner == "" || !strings.Contains(string(responses.MustMarshalJSON(fields)), catalogTestGuide) {
				t.Fatalf("additional=%v exec guidance missing: %v", additional, err)
			}
			before := responses.MustMarshalJSON(fields)
			if _, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance()); err != nil || !responses.SameJSONValue(before, responses.MustMarshalJSON(fields)) {
				t.Fatalf("namespace refresh changed stock catalog: %v", err)
			}
		}
	}

}

func TestStockExecutionAllowsHostSelectedToolSubset(t *testing.T) {
	guide := FrontendGuidance{Descriptions: catalogTestGuide}
	for _, tools := range [][]any{
		{map[string]any{"type": "function", "name": "collaboration.wait_agent"}},
	} {
		fields := map[string]jsonv1.RawMessage{"tools": responses.MustMarshalJSON(tools)}
		if _, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance()); err != nil {
			t.Fatalf("stock subset %s: %v", fields["tools"], err)
		}
	}
	description := "Run JS with tools.exec_command and collaboration tools."
	fields := map[string]jsonv1.RawMessage{
		"tools": responses.MustMarshalJSON([]any{}),
		"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(description)}),
	}
	if result, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance()); err != nil || result == "" {
		t.Fatalf("exec subset = %+v, %v", result, err)
	}
	fields = map[string]jsonv1.RawMessage{
		"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools("Run JS with tools.apply_patch.\n" + guide.Descriptions)}),
	}
	if result, err := PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, catalogTestGuidance()); err != nil || result == "" {
		t.Fatalf("patch-only exec subset = %+v, %v", result, err)
	}
	if strings.Contains(string(fields["input"]), catalogTestGuide) {
		t.Fatalf("patch-only exec retained irrelevant frontend guide: %s", fields["input"])
	}
}
