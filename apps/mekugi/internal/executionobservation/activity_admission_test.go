package executionobservation

import (
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func activityAdmissionRequest(t *testing.T, input []any) codexresponses.Request {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}
