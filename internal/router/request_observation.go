package router

import (
	"encoding/json"
)

// tokenCounts carries one provider-authoritative terminal usage observation to
// Mentor Handoff, user-only commentary, and the capturer. Aggregate reporting
// for provider metrics belongs to capture; lifetime commentary totals are separate.
type tokenCounts struct {
	InputTokens         uint64
	UncachedInputTokens uint64
	CacheWriteTokens    uint64
	OutputTokens        uint64
	ReasoningTokens     uint64

	// Preserve inconsistent raw categories before normalizing input for existing consumers.
	Inconsistent bool
	// Missing categories remain zero for existing normalized capture consumers,
	// but cannot establish complete thread totals or a reference cost.
	Incomplete bool
	// The terminal provider tier overrides the requested tier for pricing only.
	ServiceTier string
}

type requestOutcome uint8

const (
	requestOutcomeUnknown requestOutcome = iota
	requestOutcomeCompleted
	requestOutcomeFailed
	requestOutcomeCanceledBeforeResponse
	requestOutcomeCanceledAfterResponse
	requestOutcomeTimedOut
	requestOutcomeStreamIdleTimedOut
)

func (outcome requestOutcome) String() string {
	switch outcome {
	case requestOutcomeCompleted:
		return "completed"
	case requestOutcomeFailed:
		return "failed"
	case requestOutcomeCanceledBeforeResponse:
		return "canceled_before_response"
	case requestOutcomeCanceledAfterResponse:
		return "canceled_after_response"
	case requestOutcomeTimedOut:
		return "timed_out"
	case requestOutcomeStreamIdleTimedOut:
		return "stream_idle_timed_out"
	default:
		return "unknown"
	}
}

type requestObservation struct {
	outcome requestOutcome

	usageCounts   tokenCounts
	usageObserved bool
}

func usageFromResponsePayload(body []byte, streamEvent bool) (tokenCounts, bool) {
	var envelope struct {
		Type        string          `json:"type"`
		Response    json.RawMessage `json:"response"`
		Usage       json.RawMessage `json:"usage"`
		ServiceTier json.RawMessage `json:"service_tier"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return tokenCounts{}, false
	}
	raw := envelope.Usage
	tier := envelope.ServiceTier
	if streamEvent {
		switch envelope.Type {
		case "response.completed", "response.failed", "response.incomplete":
		default:
			return tokenCounts{}, false
		}
		var terminal struct {
			Usage       json.RawMessage `json:"usage"`
			ServiceTier json.RawMessage `json:"service_tier"`
		}
		if json.Unmarshal(envelope.Response, &terminal) != nil {
			return tokenCounts{}, false
		}
		raw = terminal.Usage
		tier = terminal.ServiceTier
	}
	var usage *struct {
		InputTokens  *uint64 `json:"input_tokens"`
		OutputTokens *uint64 `json:"output_tokens"`
		InputDetails struct {
			CachedTokens     *uint64         `json:"cached_tokens"`
			CacheWriteTokens json.RawMessage `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			ReasoningTokens *uint64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &usage) != nil || usage == nil {
		return tokenCounts{}, false
	}
	var counts tokenCounts
	var cached uint64
	for _, field := range []struct {
		src *uint64
		dst *uint64
	}{
		{usage.InputTokens, &counts.InputTokens},
		{usage.OutputTokens, &counts.OutputTokens},
		{usage.InputDetails.CachedTokens, &cached},
		{usage.OutputDetails.ReasoningTokens, &counts.ReasoningTokens},
	} {
		if field.src == nil {
			counts.Incomplete = true
		} else {
			*field.dst = *field.src
		}
	}
	counts.UncachedInputTokens = counts.InputTokens - min(counts.InputTokens, cached)
	// This additive field is optional for older providers (also Codex's default).
	// Explicit null or invalid evidence is not a known zero cache-write count.
	if raw := usage.InputDetails.CacheWriteTokens; len(raw) != 0 {
		var writes *uint64
		if json.Unmarshal(raw, &writes) != nil || writes == nil {
			counts.Incomplete = true
		} else {
			counts.CacheWriteTokens = *writes
		}
	}
	counts.Inconsistent = cached > counts.InputTokens || counts.ReasoningTokens > counts.OutputTokens || counts.CacheWriteTokens > counts.UncachedInputTokens
	counts.ServiceTier = usageServiceTier(tier)
	return counts, true
}

// Only an absent field permits request-tier fallback. Null, malformed, and
// unsupported provider values must not accidentally select standard prices.
func usageServiceTier(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var tier string
	if json.Unmarshal(raw, &tier) == nil {
		switch tier {
		case "default", "priority", "fast", "auto", "flex":
			return tier
		}
	}
	return "unknown"
}
