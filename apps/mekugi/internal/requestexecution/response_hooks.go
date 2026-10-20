// Source: apps/mekugi/internal/router/response_hooks.go:1:75@b9679c93.
package requestexecution

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"time"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// ResponseHooks observes the provider boundary before rewriting. It never emits
// payloads or owns a transformer, transport, subscription, or background task.
type ResponseHooks struct {
	onProviderFailure   func([]byte, bool)
	upstreamStatus      int
	Transport           string
	StreamDiagnostics   *transportcapture.Stream
	OnUsage             func(inference.Usage)
	ReceivedAt          time.Time // Body read time, before buffered events are transformed.
	deliveredResponseID string
	deliveredTerminal   bool
}

func (h *ResponseHooks) Observe(payload []byte, stream bool) {
	if h == nil || stream && len(payload) == 0 {
		return
	}
	if stream {
		h.StreamDiagnostics.Observe(payload)
	} else {
		h.ReceivedAt = time.Now()
	}
	if h.onProviderFailure != nil &&
		(!stream && h.upstreamStatus >= 400 || responses.ObserveTerminal(payload, stream) == responses.TerminalFailed) {
		h.onProviderFailure(payload, stream)
	}
	if h.OnUsage != nil {
		if usage, ok := codexresponses.UsageFromPayload(payload, stream); ok {
			h.OnUsage(usage)
		}
	}
}

func (h *ResponseHooks) Delivered(payload []byte) {
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

func (h *ResponseHooks) ReadEnded(err error, unterminated bool) {
	if h == nil {
		return
	}
	recordStreamReadEnd(h.StreamDiagnostics, err, "")
	if h.StreamDiagnostics != nil {
		h.StreamDiagnostics.UnterminatedEvent = unterminated
	}
}
