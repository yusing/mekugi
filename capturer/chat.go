package capturer

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/chat"
	"github.com/yusing/mekugi/internal/tokenizer"
)

type chatCaptureCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// observeChatResponse measures the actual Chat Completions provider boundary,
// not the router's synthesized Responses envelope. Usage still comes from the
// shared terminal observation seam; there is no second usage parser here.
func observeChatResponse(payload []byte, contentType string, record *captureRecord, codec tokenizer.Codec) []byte {
	var content strings.Builder
	calls := map[int]*chatCaptureCall{}
	finished, done := false, false
	consume := func(data []byte) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			done = true
			return
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index int `json:"index"`
						chatCaptureCall
					} `json:"tool_calls"`
				} `json:"delta"`
				Message struct {
					Content   string            `json:"content"`
					ToolCalls []chatCaptureCall `json:"tool_calls"`
				} `json:"message"`
				FinishReason chat.FinishReason `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			record.CaptureError = "invalid chat completion JSON"
			return
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			content.WriteString(choice.Message.Content)
			for _, part := range choice.Delta.ToolCalls {
				call := calls[part.Index]
				if call == nil {
					call = &chatCaptureCall{Type: "function"}
					calls[part.Index] = call
				}
				if part.ID != "" {
					call.ID = part.ID
				}
				call.Function.Name += part.Function.Name
				call.Function.Arguments += part.Function.Arguments
			}
			for i, part := range choice.Message.ToolCalls {
				calls[i] = &part
			}
			if status := choice.FinishReason.ResponseStatus(); status != "" {
				finished = true
				record.ResponseStatus = status
			}
		}
	}
	stream := strings.Contains(strings.ToLower(contentType), "text/event-stream") || capturedPayloadLooksLikeSSE(payload)
	if stream {
		for data := range sseData(payload) {
			consume(data)
		}
	} else {
		consume(bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf}))
		done = true
	}
	if !finished || !done {
		record.ResponseStatus = ""
		return nil
	}
	ordered := []chatCaptureCall{}
	for _, index := range slices.Sorted(maps.Keys(calls)) {
		call := calls[index]
		ordered = append(ordered, *call)
		encoded, _ := json.Marshal(call)
		inputTokens, e1 := codec.Count(call.Function.Arguments)
		itemTokens, e2 := contentTokens(encoded, codec)
		if e1 != nil || e2 != nil || inputTokens < 0 || itemTokens < 0 || call.ID == "" || call.Function.Name == "" {
			continue
		}
		record.ToolCalls = append(record.ToolCalls, toolCallMetrics{CallID: call.ID, Name: call.Function.Name, InputBytes: uint64(len(call.Function.Arguments)), InputTokens: uint64(inputTokens), ItemBytes: uint64(len(encoded)), ItemTokens: uint64(itemTokens)})
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if len(ordered) > 0 {
		message["tool_calls"] = ordered
	}
	output, _ := json.Marshal([]any{message})
	return output
}
