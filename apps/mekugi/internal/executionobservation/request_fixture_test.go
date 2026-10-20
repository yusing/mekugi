package executionobservation

import (
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func serverRequest(t *testing.T, mutate func(map[string]any)) codexresponses.Request {
	t.Helper()
	request := map[string]any{
		"model":     "gpt-test",
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			testFlatCodeModeAdditionalTools(testCodeModeDescription),
			map[string]any{"role": "user", "content": "task"},
		},
		"tools":       []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice": "auto",
	}
	if mutate != nil {
		mutate(request)
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
