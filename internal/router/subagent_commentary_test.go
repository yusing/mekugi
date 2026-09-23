package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSubagentCommentaryJSONIsVisibleAndRemovedFromReplay(t *testing.T) {
	responseText := "status done\nfact exact response\n"
	agentMessage := map[string]any{
		"type": "agent_message", "id": "amsg-result", "author": "/root/explorer", "recipient": "/root",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/explorer\nPayload:\n" + responseText,
		}},
	}
	transform, _, request := newSubagentCommentaryTestTransform(t, []any{agentMessage})

	spawnArguments := `{"task_name":"inspect","message":"encrypted-spawn-message","agent_type":" explorer ","model":"gpt-requested","reasoning_effort":"low"}`
	followupArguments := `{"target":"/root/explorer","message":"encrypted-follow-up-message"}`
	usage := map[string]any{
		"input_tokens": 120, "output_tokens": 30,
		"input_tokens_details":  map[string]any{"cached_tokens": 80},
		"output_tokens_details": map[string]any{"reasoning_tokens": 20},
	}
	payload := mustTestJSON(t, map[string]any{
		"id": "resp-json", "status": "completed", "usage": usage,
		"output": []any{
			map[string]any{
				"type": "function_call", "id": "item-spawn", "call_id": "call-spawn",
				"namespace": "collaboration", "name": "spawn_agent", "arguments": spawnArguments,
			},
			map[string]any{
				"type": "function_call", "id": "item-followup", "call_id": "call-followup",
				"namespace": "collaboration", "name": "followup_task", "arguments": followupArguments,
			},
			map[string]any{
				"type": "function_call", "id": "item-send", "call_id": "call-send",
				"namespace": "collaboration", "name": "send_message", "arguments": `{"target":"/root/explorer","message":"not requested"}`,
			},
		},
	})
	observeTestResponseUsage(t, transform, payload, false)
	transformed, err := transform.TransformJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(transformed, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 4 {
		t.Fatalf("output = %s", transformed)
	}
	if text := commentaryText(t, response.Output[0]); text != "[`/root/explorer` -> `/root`] Message received:\n"+responseText {
		t.Fatalf("response commentary = %q", text)
	}
	if jsonString(response.Output[1], "arguments") != spawnArguments ||
		jsonString(response.Output[2], "arguments") != followupArguments ||
		jsonString(response.Output[3], "name") != "send_message" {
		t.Fatalf("collaboration calls changed: %s", transformed)
	}

	var forwarded []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &forwarded); err != nil {
		t.Fatal(err)
	}
	if !containsAgentMessage(forwarded, "amsg-result") {
		t.Fatalf("subagent response was removed from model input: %s", request.fields["input"])
	}
	for _, item := range forwarded {
		if strings.HasPrefix(jsonString(item, "id"), subagentCommentaryMessagePrefix) {
			t.Fatalf("user-only commentary reached model input: %s", request.fields["input"])
		}
	}
}

func TestSubagentReceiptDirectionAndCompletionSummary(t *testing.T) {
	for _, test := range []struct {
		name, sender, recipient, kind, body, want string
	}{
		{"parent message", "/root", "/root/reviewer", "MESSAGE", "Please finish.", "[`/root` -> `/root/reviewer`] Message received:\nPlease finish."},
		{"child message", "/root/reviewer", "/root", "MESSAGE", "Need input.", "[`/root/reviewer` -> `/root`] Message received:\nNeed input."},
		{"sibling message", "/root/a", "/root/b", "MESSAGE", "Evidence.", "[`/root/a` -> `/root/b`] Message received:\nEvidence."},
		{"completion", "/root/reviewer", "/root", "FINAL_ANSWER", "Journal result\nQuestion: internal assignment\nAnswer: findings", ""},
		{"large completion", "/root/reviewer", "/root", "FINAL_ANSWER", strings.Repeat("findings ", maxCommentaryPublicationBytes), ""},
		{"message lookalike", "/root/reviewer", "/root", "MESSAGE", "Journal result", "[`/root/reviewer` -> `/root`] Message received:\nJournal result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := mustTestJSON(t, []any{map[string]any{
				"type": "agent_message", "id": "receipt", "author": test.sender, "recipient": test.recipient,
				"content": []any{map[string]any{"type": "input_text", "text": "Message Type: " + test.kind + "\nTask name: " + test.recipient + "\nSender: " + test.sender + "\nPayload:\n" + test.body}},
			}})
			fields := map[string]json.RawMessage{"input": input}
			messages := prepareSubagentInputCommentary(fields, test.recipient)
			if test.want == "" && len(messages) != 0 || test.want != "" && (len(messages) != 1 || commentaryText(t, messages[0]) != test.want) {
				t.Fatalf("receipt = %s", mustTestJSON(t, messages))
			}
			if !bytes.Equal(input, fields["input"]) {
				t.Fatal("receipt projection changed the native envelope")
			}
		})
	}
}

func TestTokenUsageCommentaryUsesSharedObservationWithoutReplacingTerminalMessage(t *testing.T) {
	terminal := map[string]any{
		"type": "message", "id": "msg-final", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "substantive result"}},
	}
	payload := mustTestJSON(t, map[string]any{
		"id": "resp-terminal", "status": "completed", "output": []any{terminal},
		"usage": map[string]any{
			"input_tokens": 20, "output_tokens": 5,
			"input_tokens_details":  map[string]any{"cached_tokens": 12},
			"output_tokens_details": map[string]any{"reasoning_tokens": 3},
		},
	})
	response, _, err := responseWithTokenUsageCommentary(payload, tokenUsageReport{
		InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	var output []map[string]json.RawMessage
	if err := json.Unmarshal(response["output"], &output); err != nil || len(output) != 2 {
		t.Fatalf("output = %s, error = %v", response["output"], err)
	}
	if text := commentaryText(t, output[0]); !strings.HasPrefix(text, testTokenUsageTable) {
		t.Fatalf("usage commentary = %q", text)
	}
	if jsonString(output[1], "id") != "msg-final" {
		t.Fatalf("terminal message = %s", response["output"])
	}
}

func TestSubagentResponseCommentaryDoesNotRepeat(t *testing.T) {
	responseText := "result"
	agentMessage := map[string]any{
		"type": "agent_message", "id": "amsg-result", "author": "/root/worker", "recipient": "/root",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n" + responseText,
		}},
	}
	generatedID := subagentCommentaryMessageID("response\x00amsg-result\x00/root/worker\x00" + responseText)
	generated := assistantCommentaryMessage(generatedID, "Response from /root/worker:\n"+responseText)
	input := []any{generated, agentMessage}
	transform, _, request := newSubagentCommentaryTestTransform(t, input)
	if len(transform.subagentDeferred) != 0 {
		t.Fatalf("deferred commentary = %#v", transform.subagentDeferred)
	}
	// This fixture has no retained provenance for the supplied message. Its
	// visible identity prevents a duplicate projection, but cannot authorize removal.
	if !bytes.Contains(request.fields["input"], []byte(generatedID)) {
		t.Fatalf("unretained commentary disappeared from model input: %s", request.fields["input"])
	}
}

func TestSubagentTokenUsageSilentOnFailedAndIncompleteStops(t *testing.T) {
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
				bytes.Contains(events[0], []byte("Tokens for this session")) {
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

func commentaryEventText(t *testing.T, event []byte) string {
	t.Helper()
	var envelope struct {
		Item map[string]json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(event, &envelope); err != nil {
		t.Fatal(err)
	}
	return commentaryText(t, envelope.Item)
}

func containsAgentMessage(items []map[string]json.RawMessage, id string) bool {
	for _, item := range items {
		if jsonString(item, "type") == "agent_message" && jsonString(item, "id") == id {
			return true
		}
	}
	return false
}
