package executionobservation

import (
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func prepareActivityTest(t *testing.T, proxy *Preparer, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *codexresponses.Request) {
	t.Helper()
	return prepareActivityModelTest(t, proxy, "gpt-test", session, thread, parent, name, input)
}

func prepareActivityModelTest(t *testing.T, proxy *Preparer, model, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *codexresponses.Request) {
	t.Helper()
	tools := []any{
		map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
	}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": model, "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...), "tools": tools}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread, ParentThreadID: parent, AgentName: name}
	if parent != "" {
		metadata.SubagentKind = "thread_spawn"
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, session, thread, metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform, &request
}
