package launch

import (
	"maps"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func prepareActivityTest(t *testing.T, proxy *observationFixture, session, thread, parent, name string, input []any) (requestexecution.Prepared, *codexresponses.Request) {
	t.Helper()
	return prepareActivityModelTest(t, proxy, "gpt-test", session, thread, parent, name, input)
}

func prepareActivityModelTest(t *testing.T, proxy *observationFixture, model, session, thread, parent, name string, input []any) (requestexecution.Prepared, *codexresponses.Request) {
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

// Exercise request-derived supplements through the prepared observation owner.
func observeActivityRequest(t *testing.T, p *observationFixture, thread, recipient string, request *codexresponses.Request) {
	t.Helper()
	composed := newManagedMekugiProxy(t)
	composed.activity, composed.nativeTrace = p.activity, p.nativeTrace
	copy := *request
	copy.Fields = maps.Clone(request.Fields)
	if len(copy.Fields["tools"]) == 0 {
		copy.Fields["tools"] = mustTestJSON(t, testExecResponsesTools())
	}
	metadata := codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread, AgentName: recipient}
	if thread != "main" {
		metadata.ParentThreadID = "main"
		metadata.SubagentKind = "thread_spawn"
	}
	prepared, err := composed.prepareRequest(t.Context(), &copy, "activity", thread, metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Close()
}
