package capturer

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// Observe actual Messages terminal status and block lifecycle. Token usage still comes
// from the router's existing terminal-usage seam, not a second usage parser.
func observeAnthropicResponse(payload []byte, record *captureRecord) {
	blocks := map[int]bool{}
	closed := map[int]bool{}
	status := ""
	done := false
	for data := range sseData(payload) {
		var event struct {
			Type  string                    `json:"type"`
			Index int                       `json:"index"`
			Block map[string]jsontext.Value `json:"content_block"`
			Delta struct {
				// Retain field-shape validation without assembling their values.
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
			return
		}
		switch event.Type {
		case "message_start":
			if record.ProviderResponse != nil {
				record.ProviderResponse.Model = safeProviderIdentifier(event.Message.Model)
			}
		case "content_block_start":
			if blocks[event.Index] {
				record.CaptureError = "duplicate Messages content block"
				return
			}
			blocks[event.Index] = event.Block != nil
		case "content_block_delta":
			active := blocks[event.Index]
			if !active || closed[event.Index] {
				record.CaptureError = "Messages delta has no active content block"
				return
			}
		case "content_block_stop":
			if !blocks[event.Index] || closed[event.Index] {
				record.CaptureError = "invalid Messages content completion"
				return
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
			return
		}
	}
	if !done || status == "" || len(closed) != len(blocks) {
		return
	}
	record.ResponseStatus = status
}
