package capturer

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yusing/mekugi/internal/chat"
)

// observeChatResponse observes the actual Chat Completions provider boundary,
// not the router's synthesized Responses envelope. Usage still comes from the
// shared terminal observation seam; there is no second usage parser here.
func observeChatResponse(payload []byte, contentType string, record *captureRecord) {
	// Decode content fields for shape validation, without assembling output.
	type toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	finished, done, malformed := false, false, false
	serviceTier := ""
	consume := func(data []byte) {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			done = true
			return
		}
		var chunk struct {
			ServiceTier json.RawMessage `json:"service_tier"`
			Choices     []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index int `json:"index"`
						toolCall
					} `json:"tool_calls"`
				} `json:"delta"`
				Message struct {
					Content   string     `json:"content"`
					ToolCalls []toolCall `json:"tool_calls"`
				} `json:"message"`
				FinishReason chat.FinishReason `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			malformed = true
			record.CaptureError = "invalid chat completion JSON"
			return
		}
		if len(chunk.ServiceTier) != 0 {
			serviceTier = providerServiceTier(chunk.ServiceTier)
		}
		for _, choice := range chunk.Choices {
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
		return
	}
	if record.Boundary == "provider" && record.ProviderResponse != nil && !malformed {
		record.ProviderResponse.ServiceTier = serviceTier
	}
}
