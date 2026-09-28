package router

import (
	"io"
	"slices"
	"strings"
	"testing"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

// Provider reasoning must reach Codex as it streams: an active summary item
// that closes before the answer starts, never a trailing item after it.
func TestProviderReasoningStreamsBeforeText(t *testing.T) {
	grok, err := translateChatRequest(grokTestRequest(t, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go-key"}}).services()[0]
	anthropic := &grokTranslation{openCode: &service, format: "anthropic", body: map[string]any{"model": "minimax-m3"}, tools: map[string]grokTool{"exec": {name: "exec", kind: "custom"}}}
	responses := &grokTranslation{openCode: &service, format: "responses", body: map[string]any{"model": "grok-4.6"}, tools: map[string]grokTool{"exec": {name: "exec", kind: "custom"}}}
	for _, test := range []struct {
		name     string
		tr       *grokTranslation
		upstream string
		want     string
		retained bool
	}{
		{"grok", grok, grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "Check "}}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "the probe."}}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "done"}, "finish_reason": "stop"}}},
		), "Check the probe.", false},
		{"messages", anthropic, openCodeAnthropicFixture(), "private thinking", true},
		{"responses", responses, openCodeEvents(
			map[string]any{"type": "response.created", "response": map[string]any{"model": "grok-4.6"}},
			map[string]any{"type": "response.reasoning_summary_part.added", "output_index": 0, "summary_index": 0},
			map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "First."},
			map[string]any{"type": "response.reasoning_summary_part.added", "output_index": 0, "summary_index": 1},
			map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "Second."},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs_p", "summary": []any{}, "encrypted_content": "cipher"}},
			map[string]any{"type": "response.output_text.delta", "output_index": 1, "delta": "done"},
			map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "done"}}}},
			map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}},
		), "First.\n\nSecond.", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []map[string]any
			result, err := test.tr.readProviderStream(io.NopCloser(strings.NewReader(test.upstream)), func(event map[string]any) error {
				events = append(events, event)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			kinds := func(event map[string]any) string { return event["type"].(string) }
			firstDelta := slices.IndexFunc(events, func(e map[string]any) bool { return kinds(e) == responseevents.ReasoningTextDelta })
			firstText := slices.IndexFunc(events, func(e map[string]any) bool { return kinds(e) == responseevents.OutputTextDelta })
			added := slices.IndexFunc(events, func(e map[string]any) bool {
				item, _ := e["item"].(map[string]any)
				return kinds(e) == responseevents.OutputItemAdded && item["type"] == "reasoning"
			})
			done := slices.IndexFunc(events, func(e map[string]any) bool {
				item, _ := e["item"].(map[string]any)
				return kinds(e) == responseevents.OutputItemDone && item["type"] == "reasoning"
			})
			if added < 0 || firstDelta < added || done < firstDelta || firstText >= 0 && firstText < done {
				t.Fatalf("reasoning did not stream before text: %v", events)
			}
			var streamed strings.Builder
			for _, event := range events[firstDelta:done] {
				if kinds(event) == responseevents.ReasoningTextDelta {
					if event["item_id"] != events[added]["item"].(map[string]any)["id"] || event["output_index"] != 0 {
						t.Fatalf("delta identity: %v", event)
					}
					streamed.WriteString(event["delta"].(string))
				}
			}
			if streamed.String() != test.want {
				t.Fatalf("streamed %q, want %q", streamed.String(), test.want)
			}
			// One reasoning item leads the output, carrying the visible text and,
			// on replay-bound routes, the provider's retained data.
			output := result["output"].([]any)
			item := output[0].(map[string]any)
			summary := item["summary"].([]any)
			if item["type"] != "reasoning" || len(summary) != 1 || summary[0].(map[string]string)["text"] != test.want ||
				(item["encrypted_content"] != nil) != test.retained || events[done]["output_index"] != 0 {
				t.Fatalf("reasoning item: %v", output)
			}
			for _, other := range output[1:] {
				if other.(map[string]any)["type"] == "reasoning" {
					t.Fatalf("duplicate reasoning item: %v", output)
				}
			}
			if message := output[1].(map[string]any); message["type"] != "message" {
				t.Fatalf("message index: %v", output)
			}
		})
	}
}

// Text that precedes reasoning keeps its message active; the reasoning
// completes after it rather than interleaving items.
func TestProviderReasoningAfterTextIsDeferred(t *testing.T) {
	tr, err := translateChatRequest(grokTestRequest(t, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	result, err := tr.readGrokStream(strings.NewReader(grokTestSSE(
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "answer"}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "late"}, "finish_reason": "stop"}}},
	)), func(event map[string]any) error {
		events = append(events, event["type"].(string))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(events, responseevents.ReasoningTextDelta) {
		t.Fatalf("reasoning streamed inside an active message: %v", events)
	}
	output := result["output"].([]any)
	if len(output) != 2 || output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["summary"].([]any)[0].(map[string]string)["text"] != "late" {
		t.Fatalf("deferred reasoning: %v", output)
	}
}

// Messages and Responses routes cannot replay summary-only reasoning, so a
// visible block that closes before its retained item keeps no summary.
func TestProviderReasoningWithoutRetainedItemIsNotReplayed(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go-key"}}).services()[0]
	tr := &grokTranslation{openCode: &service, format: "responses", body: map[string]any{"model": "grok-4.6"}, tools: map[string]grokTool{}}
	result, err := tr.readProviderStream(io.NopCloser(strings.NewReader(openCodeEvents(
		map[string]any{"type": "response.reasoning_text.delta", "output_index": 0, "delta": "unsigned"},
		map[string]any{"type": "response.output_text.delta", "output_index": 1, "delta": "done"},
		map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{}}},
	))), func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	history := append(result["output"].([]any), map[string]string{"role": "user", "content": "next"})
	if _, err := translateChatRequest(mustTestJSON(t, map[string]any{"model": service.prefix + ":grok-4.6", "input": history}), &service); err != nil {
		t.Fatalf("history with an unsigned visible block does not replay: %v", err)
	}
}

// Chat reasoning split around text replays whole on its assistant.
func TestOpenCodeChatReasoningAroundTextReplays(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	model := service.prefix + ":" + openCodeTestModel(service)
	history := []any{map[string]string{"role": "user", "content": "Question"}}
	tr, err := translateChatRequest(mustTestJSON(t, map[string]any{"model": model, "input": history}), &service)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tr.readGrokStream(strings.NewReader(grokTestSSE(
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "before "}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "text"}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "after"}, "finish_reason": "stop"}}},
	)), func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, result["output"].([]any)...)
	tr, err = translateChatRequest(mustTestJSON(t, map[string]any{"model": model, "input": history}), &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := tr.body["messages"].([]map[string]any)
	if len(messages) != 2 || messages[1]["reasoning_content"] != "before after" {
		t.Fatalf("reasoning around text: %v", messages)
	}
}
