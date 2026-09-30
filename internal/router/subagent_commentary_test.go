package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestChildCompletionsPreserveTerminalEvents(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			metadata := codexTurnMetadata{SubagentKind: "thread_spawn"}
			transform, _, _ := newSubagentCommentaryTestTransformWithMetadata(t, nil, metadata)
			terminal := mustTestJSON(t, map[string]any{
				"type": "response." + status,
				"response": map[string]any{
					"id": "resp-" + status, "status": status, "output": []any{},
					"usage": map[string]any{
						"input_tokens": 90, "output_tokens": 11,
						"input_tokens_details":  map[string]any{"cached_tokens": 60},
						"output_tokens_details": map[string]any{"reasoning_tokens": 7},
					},
				},
			})
			observeTestResponseUsage(t, transform, terminal, true)
			events, err := transform.TransformSSE(terminal)
			if err != nil || len(events) != 1 {
				t.Fatalf("terminal events = %q, error %v", events, err)
			}
			if !bytes.Contains(events[0], []byte(`"type":"response.`+status+`"`)) ||
				bytes.Contains(events[0], []byte("Router session usage")) {
				t.Fatalf("terminal event = %s", events[0])
			}
		})
	}
}

func observeTestResponseUsage(t *testing.T, transform *mekugiResponseTransform, payload []byte, streamEvent bool) {
	t.Helper()
	counts, observed := usageFromResponsePayload(payload, streamEvent)
	if !observed {
		t.Fatal("test response has no provider usage")
	}
	transform.observeResponseUsage(counts)
}

func newSubagentCommentaryTestTransform(
	t *testing.T,
	conversation []any,
) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest) {
	t.Helper()
	return newSubagentCommentaryTestTransformWithMetadata(t, conversation, codexTurnMetadata{})
}

func newSubagentCommentaryTestTransformWithMetadata(
	t *testing.T,
	conversation []any,
	metadata codexTurnMetadata,
) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest) {
	t.Helper()
	additional := testCodeModeAdditionalTools(testCodeModeDescription)
	namespaces := additional["tools"].([]any)
	collaboration := namespaces[1].(map[string]any)
	collaboration["tools"] = []any{
		map[string]any{"type": "function", "name": "spawn_agent"},
		map[string]any{"type": "function", "name": "followup_task"},
		map[string]any{"type": "function", "name": "send_message"},
	}
	input := append([]any{additional, map[string]any{"role": "user", "content": "task"}}, conversation...)
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-parent", "reasoning": map[string]any{"effort": "medium"}, "input": input,
		"tools":       []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice": "auto", "parallel_tool_calls": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	workspace := t.TempDir()
	metadata.RequestKind = "turn"
	metadata.Directories = map[string]json.RawMessage{workspace: nil}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	if transform == nil {
		t.Fatal("prepareRequest returned no transform")
	}
	t.Cleanup(transform.Close)
	return transform, proxy, &request
}

func commentaryText(t *testing.T, item map[string]json.RawMessage) string {
	t.Helper()
	if jsonString(item, "type") != "message" || jsonString(item, "phase") != "commentary" {
		t.Fatalf("not commentary: %#v", item)
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(item["content"], &content); err != nil || len(content) != 1 {
		t.Fatalf("commentary content = %s, error %v", item["content"], err)
	}
	return jsonString(content[0], "text")
}
