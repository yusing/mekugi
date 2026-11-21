package launch

import (
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
