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
				t.Setenv("TMPDIR", t.TempDir())
				metadata := codexTurnMetadata{}
				if child {
					metadata.SubagentKind = threadSpawnSubagentKind
				}
				transform, _, _ := newSubagentCommentaryTestTransformWithMetadata(t, nil, metadata)
				transform.journalActive = false
				answer := finalAnswerTestEvents(t, phase)
				terminal := finalAnswerTestTerminal(t, "completed", true)
				var output bytes.Buffer
				state, err := copySSETransformed(&output, strings.NewReader(finalAnswerTestWire(append(slices.Clone(answer), terminal))),
					transform, &responseHooks{onUsage: transform.observeResponseUsage})
				if err != nil || state != responseTerminalCompleted {
					t.Fatalf("state=%v, error=%v", state, err)
				}
				events := finalAnswerTestPayloads(output.String())
				// Without an active journal, the answer lifecycle is streamed
				// unchanged; completion metrics remain outside the conversation.
				wantEvents := len(answer) + 1
				if len(events) != wantEvents {
					t.Fatalf("events = %s", output.String())
				}
				if bytes.Contains(output.Bytes(), []byte("Router session usage")) {
					t.Fatalf("completion exposed usage commentary: %s", output.String())
				}
				if !bytes.Contains(output.Bytes(), []byte("No files were changed.")) {
					t.Fatal("provider answer was filtered")
				}

				// Model Codex's event handling: the provider final answer is the
				// last completed assistant item before the response terminal.
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
				if !completed || len(rendered) != 1 || lastAgentMessage != "No files were changed." {
					t.Fatalf("Codex result = %q, rendered=%q, completed=%v", lastAgentMessage, rendered, completed)
				}
			})
		}
	}
}

func TestFinalAnswerStreamBuffersAnswersButStreamsProgressAndTools(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
	transform.journalActive = false
	answer := finalAnswerTestEvents(t, "final_answer")
	var observation finalAnswerStream
	for _, event := range answer {
		if visible, buffered := observation.observe(event); !buffered || len(visible) != 0 {
			t.Fatalf("answer was not buffered: %q", visible)
		}
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 0 {
			t.Fatalf("answer escaped before terminal: %q, %v", visible, err)
		}
	}
	if !observation.substantive || !transform.finalAnswer.substantive {
		t.Fatal("answer lifecycle did not retain substantive-completion evidence")
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
	if err != nil || len(visible) != len(answer)+1 || bytes.Contains(bytes.Join(visible, nil), []byte("Router session usage")) {
		t.Fatalf("client dispatch produced usage or lost answer: %q, %v", visible, err)
	}
}

func TestFinalAnswerStreamJournalBufferFlushesUnchanged(t *testing.T) {
	var stream finalAnswerStream
	expected := finalAnswerTestEvents(t, "final_answer")
	for index, event := range expected {
		visible, buffered := stream.observe(event)
		if !buffered || len(visible) != 0 {
			t.Fatalf("journal answer event %d escaped before terminal delivery: %q", index, visible)
		}
	}
	if got := stream.flush(); len(got) != len(expected) {
		t.Fatalf("flushed %d answer events, want %d", len(got), len(expected))
	} else {
		for i := range expected {
			if !bytes.Equal(got[i], expected[i]) {
				t.Fatalf("flushed event %d changed: %s", i, got[i])
			}
		}
	}
}

func TestFinalAnswerStreamFlushesWithoutUsage(t *testing.T) {
	failure := errors.New("upstream disconnected")
	for _, stop := range []string{"missing_usage", "failed", "incomplete", "eof", "read_error", "error_event", "done_sentinel"} {
		t.Run(stop, func(t *testing.T) {
			transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
			transform.journalActive = false
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
			_, err := copySSETransformed(&output, reader, chain, &responseHooks{onUsage: transform.observeResponseUsage})
			if stop == "read_error" {
				if !errors.Is(err, failure) {
					t.Fatalf("lost upstream error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			events := finalAnswerTestPayloads(output.String())
			if len(events) < len(answer) || bytes.Contains(output.Bytes(), []byte("Router session usage")) {
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

type finalAnswerErrorReader struct{ err error }

func (r finalAnswerErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestFinalAnswerStreamBudgetPreservesOutput(t *testing.T) {
	var stream finalAnswerStream
	events := finalAnswerTestEvents(t, "final_answer")
	if visible, buffered := stream.observe(events[0]); !buffered || len(visible) != 0 {
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

func TestNonJournalAnswerStreamsWithoutUsageCommentary(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "child"}[child], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			proxy := newManagedMekugiProxy(t)
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
			if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, nil, proxy, nil); err != nil {
				t.Fatal(err)
			}
			events := finalAnswerTestPayloads(output.String())
			if len(events) != 2 || bytes.Contains(output.Bytes(), []byte("Router session usage")) {
				t.Fatalf("completion output = %s", output.String())
			}
			if !bytes.Contains(output.Bytes(), []byte("No files were changed.")) || bytes.Contains(output.Bytes(), []byte(`"id":"answer"`)) {
				t.Fatal("journal completion did not capture the provider answer")
			}
			counts, available := proxy.usage.snapshot("thread-1")
			if !available || counts.tokenCounts != (tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3}) {
				t.Fatalf("provider usage changed: %+v, available=%v", counts, available)
			}
			if paths := proxy.tokenMetricPaths(); len(paths) != map[bool]int{false: 1, true: 0}[child] {
				t.Fatalf("child=%t token metric paths = %q", child, paths)
			}
		})
	}
}
