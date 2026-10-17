package binding

import (
	jsonv1 "encoding/json"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestMissingContinuationToolDoesNotInventAuthority(t *testing.T) {
	tools := executionContinuationTools{}
	for _, continuation := range []ExecutionContinuation{tools.forCell("cell-1"), tools.forSession(42)} {
		if continuation.NextCall != nil || continuation.Reason == "" {
			t.Fatalf("invented continuation: %+v", continuation)
		}
	}
}

func TestExecutionContinuationLongWaitTiming(t *testing.T) {
	for _, name := range []string{"wait", "write_stdin"} {
		for _, limit := range []struct {
			maximum jsonv1.Number
			want    int
		}{
			{"0", 300000},
			{"120000", 120000},
			{"120000.0", 120000},
			{"120000.5", 120000},
			{"300000", 300000},
			{"600000", 300000},
		} {
			t.Run(name+"/"+string(limit.maximum), func(t *testing.T) {
				properties := map[string]any{
					"cell_id":       map[string]any{"type": "string"},
					"session_id":    map[string]any{"type": "integer"},
					"chars":         map[string]any{"type": "string"},
					"yield_time_ms": map[string]any{"type": "integer", "maximum": limit.maximum},
					"unrelated":     map[string]any{"type": "number", "maximum": 1.5},
				}
				catalog := codexresponses.DecodeToolCatalog(map[string]jsonv1.RawMessage{
					"tools": responses.MustMarshalJSON([]any{map[string]any{
						"type": "function", "name": name,
						"parameters": map[string]any{"type": "object", "properties": properties},
					}}),
				})
				tools := executionTools(catalog, "exec")
				result := tools.forCell("cell-1")
				if name == "write_stdin" {
					result = tools.forSession(42)
				}
				want := limit.want
				if result.NextCall == nil || result.NextCall.Input.(map[string]any)["yield_time_ms"] != want {
					t.Fatalf("continuation = %+v, want wait %d", result, want)
				}
			})
		}
	}
}

func TestExecutionContinuationNestedLongWait(t *testing.T) {
	description := "### `write_stdin`\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string; yield_time_ms?: number }): Promise<unknown>; };\n"
	catalog := codexresponses.DecodeToolCatalog(map[string]jsonv1.RawMessage{
		"tools": responses.MustMarshalJSON([]any{
			map[string]any{"type": "custom", "name": "exec", "description": description},
		}),
	})
	result := executionTools(catalog, "exec").forSession(42)
	want := `text(await tools.write_stdin({"chars":"","session_id":42,"yield_time_ms":300000}));`
	if result.NextCall == nil || result.NextCall.Input != want {
		t.Fatalf("nested continuation = %+v, want %s", result, want)
	}
}
