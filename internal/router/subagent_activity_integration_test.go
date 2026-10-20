package router

import (
	"testing"
)

func prepareActivityTest(t *testing.T, proxy *mekugiProxy, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	return prepareActivityModelTest(t, proxy, "gpt-test", session, thread, parent, name, input)
}

func prepareActivityModelTest(t *testing.T, proxy *mekugiProxy, model, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	tools := []any{
		map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
			map[string]any{"type": "function", "name": "send_message"}, map[string]any{"type": "function", "name": "followup_task"},
		}},
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": model, "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, input...), "tools": tools}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexTurnMetadata{RequestKind: "turn", ThreadID: thread, ParentThreadID: parent, AgentName: name}
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
