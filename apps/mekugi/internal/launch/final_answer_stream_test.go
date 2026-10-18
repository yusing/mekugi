package launch

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
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

func TestAnswerStreamsWithoutUsageCommentary(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "child"}[child], func(t *testing.T) {

			proxy := newManagedMekugiProxy(t)
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
			if child {
				var metadata codexresponses.TurnMetadata
				if err := json.Unmarshal([]byte(headers.Get(codexresponses.TurnMetadataHeader)), &metadata); err != nil {
					t.Fatal(err)
				}
				metadata.SubagentKind = threadSpawnSubagentKind
				headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
			}
			answer := finalAnswerTestEvents(t, "final_answer")
			terminal := finalAnswerTestTerminal(t, "completed", true)
			response := serverHTTPResponse(finalAnswerTestWire(append(slices.Clone(answer), terminal)))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			events := finalAnswerTestPayloads(output.String())
			wantEvents := len(answer) + 1
			if len(events) != wantEvents || bytes.Contains(output.Bytes(), []byte("Router session usage")) {
				t.Fatalf("completion output = %s", output.String())
			}
			if !bytes.Contains(output.Bytes(), []byte("No files were changed.")) || !bytes.Contains(output.Bytes(), []byte(`"id":"answer"`)) {
				t.Fatal("completion lost the answer or violated Main/child delivery ownership")
			}
			counts, available := proxy.usage.Snapshot("thread-1")
			if !available || counts.Usage != (inference.Usage{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3}) {
				t.Fatalf("provider usage changed: %+v, available=%v", counts, available)
			}
		})
	}
}
