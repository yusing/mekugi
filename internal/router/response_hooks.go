package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// responseHooks observes the provider boundary before rewriting. It never emits
// payloads or owns a transformer, transport, subscription, or background task.
type responseHooks struct {
	onProviderFailure   func([]byte, bool)
	upstreamStatus      int
	streamDiagnostics   *streamDiagnostics
	onUsage             func(inference.Usage)
	receivedAt          time.Time // Body read time, before buffered events are transformed.
	deliveredResponseID string
	deliveredTerminal   bool
}

func (h *responseHooks) Observe(payload []byte, stream bool) {
	if h == nil || stream && len(payload) == 0 {
		return
	}
	if stream {
		h.streamDiagnostics.observe(payload)
	} else {
		h.receivedAt = time.Now()
	}
	if h.onProviderFailure != nil &&
		(!stream && h.upstreamStatus >= 400 || responses.ObserveTerminal(payload, stream) == responses.TerminalFailed) {
		h.onProviderFailure(payload, stream)
	}
	if h.onUsage != nil {
		if usage, ok := codexresponses.UsageFromPayload(payload, stream); ok {
			h.onUsage(usage)
		}
	}
}

func (h *responseHooks) Delivered(payload []byte) {
	if h == nil {
		return
	}
	var event struct {
		Type     string `json:"type"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &event, jsonv1.DefaultOptionsV1()) == nil && event.Type == "response.created" {
		h.deliveredResponseID = event.Response.ID
	}
	if responses.ObserveTerminal(payload, true).Terminal() {
		h.deliveredTerminal = true
	}
}

func (h *responseHooks) ReadEnded(err error, unterminated bool) {
	if h == nil {
		return
	}
	h.streamDiagnostics.readEnded(err)
	if h.streamDiagnostics != nil {
		h.streamDiagnostics.UnterminatedEvent = unterminated
	}
}
