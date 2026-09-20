package router

import (
	"encoding/json"

	"github.com/yusing/mekugi/internal/responses"
)

// requestCompletion keeps accepted output distinct from a successful terminal.
// In particular, steering accepts output but must not reset a compaction schedule.
type requestCompletion struct {
	outcome       requestOutcome
	terminal      responseTerminalState
	usage         tokenCounts
	usageObserved bool
}

func (r requestCompletion) acceptsOutput() bool { return r.outcome == requestOutcomeCompleted }
func (r requestCompletion) succeeded() bool {
	return r.acceptsOutput() && r.terminal == responseTerminalCompleted
}

type responseOutputObserver interface {
	outputItemDone(json.RawMessage)
	completedOutput([]json.RawMessage)
}

// responseHooks observes the provider boundary before rewriting. It never emits
// payloads or owns a transformer, transport, subscription, or background task.
type responseHooks struct {
	onProviderFailure func([]byte, bool)
	streamDiagnostics *streamDiagnostics
	onUsage           func(tokenCounts)
	output            responseOutputObserver
	onFinished        func(requestCompletion)
	finished          bool
}

func (h *responseHooks) finish(result requestCompletion) {
	if h == nil || h.finished {
		return
	}
	h.finished = true
	if h.onFinished != nil {
		h.onFinished(result)
	}
}

func (h *responseHooks) observe(payload []byte, stream bool) error {
	if h == nil || stream && len(payload) == 0 {
		return nil
	}
	if stream {
		h.streamDiagnostics.observe(payload)
	}
	if h.onProviderFailure != nil && responses.ObserveTerminal(payload, stream) == responses.TerminalFailed {
		h.onProviderFailure(payload, stream)
	}
	if h.onUsage != nil {
		if usage, ok := usageFromResponsePayload(payload, stream); ok {
			h.onUsage(usage)
		}
	}
	if h.output == nil {
		return nil
	}
	if stream && string(payload) == "[DONE]" {
		return nil
	}
	// Source: mentor_handoff.go:205:239. Preserve item-done observation and
	// terminal-snapshot fallback without making Mentor parse wire envelopes.
	var output struct {
		Output []json.RawMessage `json:"output"`
	}
	if !stream {
		if err := json.Unmarshal(payload, &output); err != nil {
			return err
		}
		h.output.completedOutput(output.Output)
		return nil
	}
	var event struct {
		Type     responses.Kind  `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.Type == responses.OutputItemDone {
		h.output.outputItemDone(event.Item)
	}
	if terminal := responses.ObserveTerminal(payload, true); terminal == responses.TerminalCompleted || terminal == responses.TerminalSteered {
		h.output.completedOutput(event.Response.Output)
	}
	return nil
}

func (f *requestFinalization) completion() requestCompletion {
	return requestCompletion{
		outcome: f.observation.outcome, terminal: f.upstreamTerminalState,
		usage: f.observation.usageCounts, usageObserved: f.observation.usageObserved,
	}
}
