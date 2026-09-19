package capturer

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"slices"

	"github.com/yusing/mekugi/internal/tokenizer"
)

// Observe actual Messages transport/output shapes. Token usage still comes
// from the router's existing terminal-usage seam, not a second usage parser.
func observeAnthropicResponse(payload []byte, record *captureRecord, codec tokenizer.Codec) []byte {
	blocks := map[int]map[string]jsontext.Value{}
	arguments := map[int]string{}
	closed := map[int]bool{}
	status := ""
	done := false
	for data := range sseData(payload) {
		var event struct {
			Type  string                    `json:"type"`
			Index int                       `json:"index"`
			Block map[string]jsontext.Value `json:"content_block"`
			Delta struct {
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Text      string `json:"text"`
				JSON      string `json:"partial_json"`
				Stop      string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(data, &event) != nil {
			record.CaptureError = "invalid Messages event JSON"
			return nil
		}
		switch event.Type {
		case "message_start":
			if record.ProviderResponse != nil {
				record.ProviderResponse.Model = safeProviderIdentifier(event.Message.Model)
			}
		case "content_block_start":
			if blocks[event.Index] != nil {
				record.CaptureError = "duplicate Messages content block"
				return nil
			}
			blocks[event.Index] = event.Block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil || closed[event.Index] {
				record.CaptureError = "Messages delta has no active content block"
				return nil
			}
			if event.Delta.Text != "" {
				var text string
				_ = json.Unmarshal(block["text"], &text)
				block["text"], _ = json.Marshal(text + event.Delta.Text)
			}
			for key, delta := range map[string]string{"thinking": event.Delta.Thinking, "signature": event.Delta.Signature} {
				if delta != "" {
					var previous string
					_ = json.Unmarshal(block[key], &previous)
					block[key], _ = json.Marshal(previous + delta)
				}
			}
			arguments[event.Index] += event.Delta.JSON
		case "content_block_stop":
			if blocks[event.Index] == nil || closed[event.Index] {
				record.CaptureError = "invalid Messages content completion"
				return nil
			}
			closed[event.Index] = true
		case "message_delta":
			switch event.Delta.Stop {
			case "end_turn", "stop_sequence", "tool_use":
				status = "completed"
			case "max_tokens", "refusal":
				status = "incomplete"
			}
		case "message_stop":
			done = true
		case "error":
			record.ResponseStatus = "failed"
			return nil
		}
	}
	if !done || status == "" || len(closed) != len(blocks) {
		return nil
	}
	var output []any
	for _, index := range slices.Sorted(maps.Keys(blocks)) {
		block := blocks[index]
		var kind, id, name string
		_ = json.Unmarshal(block["type"], &kind)
		switch kind {
		case "text", "thinking", "redacted_thinking":
			output = append(output, block)
		case "tool_use":
			_ = json.Unmarshal(block["id"], &id)
			_ = json.Unmarshal(block["name"], &name)
			input := block["input"]
			if arguments[index] != "" {
				input = jsontext.Value(arguments[index])
				block["input"] = input
			}
			if !input.IsValid() || id == "" || name == "" {
				record.CaptureError = "invalid Messages tool input"
				return nil
			}
			item, err := json.Marshal(block)
			inputTokens, inputErr := contentTokens(input, codec)
			itemTokens, itemErr := contentTokens(item, codec)
			if err != nil || inputErr != nil || itemErr != nil || inputTokens < 0 || itemTokens < 0 {
				record.CaptureError = "measure Messages tool input"
				return nil
			}
			record.ToolCalls = append(record.ToolCalls, toolCallMetrics{
				CallID: id, Name: name, InputBytes: uint64(len(input)), InputTokens: uint64(inputTokens),
				ItemBytes: uint64(len(item)), ItemTokens: uint64(itemTokens),
			})
			output = append(output, block)
		}
	}
	record.ResponseStatus = status
	result, err := json.Marshal([]any{map[string]any{"role": "assistant", "content": output}}, json.Deterministic(true))
	if err != nil {
		record.CaptureError = "encode Messages output"
		return nil
	}
	return result
}
