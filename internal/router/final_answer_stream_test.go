package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func finalAnswerTestEvents(t *testing.T, phase string) [][]byte {
	t.Helper()
	item := map[string]any{
		"type": "message", "id": "answer", "role": "assistant", "phase": phase, "status": "in_progress", "content": []any{},
	}
	if phase == "" {
		delete(item, "phase")
	}
	added := mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	part := map[string]any{"type": "output_text", "text": "No files were changed."}
	item["status"], item["content"] = "completed", []any{part}
	return [][]byte{
		added,
		mustTestJSON(t, map[string]any{"type": "response.content_part.added", "item_id": "answer", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}}),
		mustTestJSON(t, map[string]any{"type": "response.output_text.delta", "item_id": "answer", "output_index": 0, "content_index": 0, "delta": "No files were changed."}),
		mustTestJSON(t, map[string]any{"type": "response.output_text.done", "item_id": "answer", "output_index": 0, "content_index": 0, "text": "No files were changed."}),
		mustTestJSON(t, map[string]any{"type": "response.content_part.done", "item_id": "answer", "output_index": 0, "content_index": 0, "part": part}),
		mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}),
	}
}

func finalAnswerTestTerminal(t *testing.T, status string, usage bool) []byte {
	t.Helper()
	response := map[string]any{"id": "response", "status": status, "output": []any{}}
	if usage {
		response["usage"] = map[string]any{
			"input_tokens": 20, "output_tokens": 5,
			"input_tokens_details":  map[string]any{"cached_tokens": 12},
			"output_tokens_details": map[string]any{"reasoning_tokens": 3},
		}
	}
	return mustTestJSON(t, map[string]any{"type": "response." + status, "response": response})
}

func finalAnswerTestWire(events [][]byte) string {
	var wire strings.Builder
	for _, event := range events {
		wire.WriteString("data: ")
		wire.Write(event)
		wire.WriteString("\n\n")
	}
	return wire.String()
}

func finalAnswerTestPayloads(wire string) [][]byte {
	var events [][]byte
	for line := range strings.SplitSeq(wire, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			events = append(events, []byte(data))
		}
	}
	return events
}

func TestFinalAnswerStreamCodexCompletion(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, phase := range []string{"final_answer", ""} {
			t.Run(map[bool]string{false: "root", true: "child"}[child]+"/"+phase, func(t *testing.T) {
				metadata := codexTurnMetadata{}
				if child {
					metadata.SubagentKind = threadSpawnSubagentKind
				}
				transform, _, _ := newSubagentCommentaryTestTransformWithMetadata(t, nil, metadata)
				answer := finalAnswerTestEvents(t, phase)
				terminal := finalAnswerTestTerminal(t, "completed", true)
				var output bytes.Buffer
				state, err := copySSETransformed(&output, strings.NewReader(finalAnswerTestWire(append(slices.Clone(answer), terminal))),
					transform, transform.observeResponseUsage)
				if err != nil || state != responseTerminalCompleted {
					t.Fatalf("state=%v, error=%v", state, err)
				}
				events := finalAnswerTestPayloads(output.String())
				wantEvents := len(answer) + 2
				if len(events) != wantEvents {
					t.Fatalf("events = %s", output.String())
				}
				if text := commentaryEventText(t, events[0]); !strings.HasPrefix(text, testTokenUsageTable) {
					t.Fatalf("usage = %q", text)
				}
				if !bytes.Contains(output.Bytes(), []byte("No files were changed.")) {
					t.Fatal("provider answer was filtered")
				}

				// Model Codex's event handling: every done assistant item updates
				// last_agent_message, including commentary. Only completed stops it.
				var lastAgentMessage string
				completed := false
				var rendered []string
				for _, payload := range events {
					var event struct {
						Type string                     `json:"type"`
						Item map[string]json.RawMessage `json:"item"`
					}
					if err := json.Unmarshal(payload, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "response.completed" {
						completed = true
						break
					}
					if event.Type == "response.output_item.done" && jsonString(event.Item, "role") == "assistant" {
						var content []struct {
							Text string `json:"text"`
						}
						if err := json.Unmarshal(event.Item["content"], &content); err != nil || len(content) != 1 {
							t.Fatalf("invalid message content: %s", event.Item["content"])
						}
						lastAgentMessage = content[0].Text
						rendered = append(rendered, lastAgentMessage)
					}
				}
				wantMessages := 2
				wantLast := "No files were changed."
				if !completed || len(rendered) != wantMessages || lastAgentMessage != wantLast {
					t.Fatalf("Codex result = %q, rendered=%q, completed=%v", lastAgentMessage, rendered, completed)
				}
			})
		}
	}
}

func TestFinalAnswerStreamKeepsProgressAndToolsLive(t *testing.T) {
	transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
	answer := finalAnswerTestEvents(t, "final_answer")
	for _, event := range answer {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 0 {
			t.Fatalf("answer was not buffered: %q, %v", visible, err)
		}
	}
	for _, event := range [][]byte{
		assistantCommentaryDoneEvent(assistantCommentaryMessage("progress", "Still working")),
		mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "id": "tool", "call_id": "tool", "name": "lookup", "arguments": "{}",
		}}),
	} {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], event) {
			t.Fatalf("progress/tool did not stream: %q, %v", visible, err)
		}
	}
	terminal := finalAnswerTestTerminal(t, "completed", true)
	observeTestResponseUsage(t, transform, terminal, true)
	visible, err := transform.TransformSSE(terminal)
	if err != nil || len(visible) != len(answer)+1 || bytes.Contains(bytes.Join(visible, nil), []byte("Tokens:")) {
		t.Fatalf("client dispatch produced usage or lost answer: %q, %v", visible, err)
	}
}

func TestFinalAnswerStreamFlushesWithoutUsage(t *testing.T) {
	failure := errors.New("upstream disconnected")
	for _, stop := range []string{"missing_usage", "failed", "incomplete", "eof", "read_error", "error_event", "done_sentinel"} {
		t.Run(stop, func(t *testing.T) {
			transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
			answer := finalAnswerTestEvents(t, "final_answer")
			input := slices.Clone(answer)
			switch stop {
			case "missing_usage":
				input = append(input, finalAnswerTestTerminal(t, "completed", false))
			case "failed", "incomplete":
				input = append(input, finalAnswerTestTerminal(t, stop, true))
			case "done_sentinel":
				input = append(input, []byte("[DONE]"))
			case "error_event":
				input = append(input, []byte(`{"type":"error","message":"disconnected"}`))
			}
			var reader io.Reader = strings.NewReader(finalAnswerTestWire(input))
			if stop == "read_error" {
				reader = io.MultiReader(reader, finalAnswerErrorReader{failure})
			}
			var output bytes.Buffer
			// Include a real downstream transform to exercise composed draining.
			chain := composeResponseTransformers(transform, &criticalErrorTransform{})
			_, err := copySSETransformed(&output, reader, chain, transform.observeResponseUsage)
			if stop == "read_error" {
				if !errors.Is(err, failure) {
					t.Fatalf("lost upstream error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			events := finalAnswerTestPayloads(output.String())
			if len(events) < len(answer) || bytes.Contains(output.Bytes(), []byte("Tokens:")) {
				t.Fatalf("lost answer or emitted usage: %s", output.String())
			}
			for i, original := range answer {
				if !bytes.Equal(events[i], original) {
					t.Fatalf("answer event %d changed: %s", i, events[i])
				}
			}
		})
	}
}

func TestTokenCommentaryAnswerCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		item map[string]any
		want bool
	}{
		{"text", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}, true},
		{"legacy", map[string]any{"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}, true},
		{"null_phase", map[string]any{"type": "message", "role": "assistant", "phase": nil,
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}, true},
		{"empty_phase", map[string]any{"type": "message", "role": "assistant", "phase": "",
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}, false},
		{"refusal", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "refusal", "refusal": "Cannot do that."}}}, false},
		{"mixed_refusal", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"},
				map[string]any{"type": "refusal", "refusal": "Cannot do that."}}}, false},
		{"invalid_text", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": "Answer"},
				map[string]any{"type": "output_text", "text": 42}}}, false},
		{"empty", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": " "}}}, false},
		{"commentary", map[string]any{"type": "message", "role": "assistant", "phase": "commentary",
			"content": []any{map[string]any{"type": "output_text", "text": "Still working"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
					terminal := finalAnswerTestTerminal(t, "completed", true)
					var output []byte
					if stream {
						item := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": tc.item})
						initial, err := transform.TransformSSE(item)
						if err != nil {
							t.Fatal(err)
						}
						observeTestResponseUsage(t, transform, terminal, true)
						events, err := transform.TransformSSE(terminal)
						if err != nil {
							t.Fatal(err)
						}
						output = bytes.Join(append(initial, events...), nil)
						if !bytes.Contains(output, mustTestJSON(t, tc.item)) {
							t.Fatal("streamed provider output was filtered")
						}
					} else {
						var envelope struct {
							Response map[string]json.RawMessage `json:"response"`
						}
						if err := json.Unmarshal(terminal, &envelope); err != nil {
							t.Fatal(err)
						}
						envelope.Response["output"] = mustTestJSON(t, []any{tc.item})
						response := mustTestJSON(t, envelope.Response)
						observeTestResponseUsage(t, transform, response, false)
						var err error
						output, err = transform.TransformJSON(response)
						if err != nil {
							t.Fatal(err)
						}
						var visible struct {
							Output []json.RawMessage `json:"output"`
						}
						if err := json.Unmarshal(output, &visible); err != nil {
							t.Fatal(err)
						}
						if len(visible.Output) == 0 || !bytes.Equal(visible.Output[len(visible.Output)-1], mustTestJSON(t, tc.item)) {
							t.Fatalf("unsupported provider output changed: %s", output)
						}
					}
					if bytes.Contains(output, []byte("Tokens:")) != tc.want {
						t.Fatalf("usage eligibility: %s", output)
					}
				})
			}

		})
	}
}

type finalAnswerErrorReader struct{ err error }

func (r finalAnswerErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestFinalAnswerStreamBudgetPreservesOutput(t *testing.T) {
	var stream finalAnswerStream
	events := finalAnswerTestEvents(t, "final_answer")
	if _, buffered := stream.observe(events[0]); !buffered {
		t.Fatal("answer not buffered")
	}
	stream.bytes = upstreamJSONBufferBytes
	visible, buffered := stream.observe(events[1])
	if !buffered || !stream.disabled || len(visible) != 2 ||
		!bytes.Equal(visible[0], events[0]) || !bytes.Equal(visible[1], events[1]) {
		t.Fatal("buffer exhaustion lost provider events")
	}
	if _, buffered := stream.observe(events[2]); buffered {
		t.Fatal("buffering resumed after exhaustion")
	}
}

func TestFinalAnswerStreamExecuteRequest(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "child"}[child], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
			if child {
				var metadata codexTurnMetadata
				if err := json.Unmarshal([]byte(headers.Get(codexTurnMetadataHeader)), &metadata); err != nil {
					t.Fatal(err)
				}
				metadata.SubagentKind = threadSpawnSubagentKind
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
			}
			answer := finalAnswerTestEvents(t, "final_answer")
			terminal := finalAnswerTestTerminal(t, "completed", true)
			response := serverHTTPResponse(finalAnswerTestWire(append(slices.Clone(answer), terminal)))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, nil, proxy, nil, nil); err != nil {
				t.Fatal(err)
			}
			events := finalAnswerTestPayloads(output.String())
			wantEvents := len(answer) + 2
			if len(events) != wantEvents || !strings.HasPrefix(commentaryEventText(t, events[0]), "Tokens:") {
				t.Fatalf("completion output = %s", output.String())
			}
			if !bytes.Contains(output.Bytes(), []byte("No files were changed.")) || bytes.Contains(output.Bytes(), []byte("Journal saved:")) {
				t.Fatal("provider answer was filtered or mistaken for journal finish")
			}
			counts, available := proxy.usage.snapshot("thread-1")
			if !available || counts.tokenCounts != (tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3}) {
				t.Fatalf("provider usage changed: %+v, available=%v", counts, available)
			}
		})
	}
}
