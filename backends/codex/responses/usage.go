package responses

// Source: internal/router/request_observation.go:68:155@47bcdd8e terminal usage.
import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/contracts/inference"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func UsageFromPayload(body []byte, streamEvent bool) (inference.Usage, bool) {
	var envelope struct {
		Type        responseevents.Kind `json:"type"`
		Response    jsontext.Value      `json:"response"`
		Usage       jsontext.Value      `json:"usage"`
		ServiceTier jsontext.Value      `json:"service_tier"`
	}
	if json.Unmarshal(body, &envelope, jsonv1.DefaultOptionsV1()) != nil {
		return inference.Usage{}, false
	}
	raw := envelope.Usage
	tier := envelope.ServiceTier
	if streamEvent {
		if !envelope.Type.Terminal() {
			return inference.Usage{}, false
		}
		var terminal struct {
			Usage       jsontext.Value `json:"usage"`
			ServiceTier jsontext.Value `json:"service_tier"`
		}
		if json.Unmarshal(envelope.Response, &terminal, jsonv1.DefaultOptionsV1()) != nil {
			return inference.Usage{}, false
		}
		raw = terminal.Usage
		tier = terminal.ServiceTier
	}
	var usage *struct {
		InputTokens  *uint64 `json:"input_tokens"`
		OutputTokens *uint64 `json:"output_tokens"`
		InputDetails struct {
			CachedTokens     *uint64        `json:"cached_tokens"`
			CacheWriteTokens jsontext.Value `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			ReasoningTokens *uint64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &usage, jsonv1.DefaultOptionsV1()) != nil || usage == nil {
		return inference.Usage{}, false
	}
	counts := inference.Usage{TotalsKnown: usage.InputTokens != nil && usage.OutputTokens != nil}
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
		if json.Unmarshal(raw, &writes, jsonv1.DefaultOptionsV1()) != nil || writes == nil {
			counts.Incomplete = true
		} else {
			counts.CacheWriteTokens = *writes
		}
	}
	counts.Inconsistent = cached > counts.InputTokens || counts.ReasoningTokens > counts.OutputTokens || counts.CacheWriteTokens > counts.UncachedInputTokens
	counts.ServiceTier = UsageServiceTier(tier)
	return counts, true
}

// Only an absent field permits request-tier fallback. Null, malformed, and
// unsupported provider values must not accidentally select standard prices.
func UsageServiceTier(raw jsontext.Value) string {
	if len(raw) == 0 {
		return ""
	}
	var tier string
	if json.Unmarshal(raw, &tier, jsonv1.DefaultOptionsV1()) == nil {
		switch tier {
		case "default", "priority", "fast", "auto", "flex":
			return tier
		}
	}
	return "unknown"
}
