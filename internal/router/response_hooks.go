package router

import (
	"time"

	"github.com/yusing/mekugi/internal/responses"
)

// responseHooks observes the provider boundary before rewriting. It never emits
// payloads or owns a transformer, transport, subscription, or background task.
type responseHooks struct {
	onProviderFailure   func([]byte, bool)
	upstreamStatus      int
	streamDiagnostics   *streamDiagnostics
	onUsage             func(tokenCounts)
	onOutput            func([]byte)
	receivedAt          time.Time // Body read time, before buffered events are transformed.
	deliveredResponseID string
	deliveredTerminal   bool
}

func (h *responseHooks) observe(payload []byte, stream bool) error {
	if h == nil || stream && len(payload) == 0 {
		return nil
	}
	if stream {
		h.streamDiagnostics.observe(payload)
		if h.onOutput != nil {
			h.onOutput(payload)
		}
	}
	if h.onProviderFailure != nil &&
		(!stream && h.upstreamStatus >= 400 || responses.ObserveTerminal(payload, stream) == responses.TerminalFailed) {
		h.onProviderFailure(payload, stream)
	}
	if h.onUsage != nil {
		if usage, ok := usageFromResponsePayload(payload, stream); ok {
			h.onUsage(usage)
		}
	}
	return nil
}
