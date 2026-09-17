package capturer

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// providerResponseEvidence contains only allowlisted provider telemetry, never
// arbitrary headers, response text, credentials, or routing tokens.
type providerResponseEvidence struct {
	wireFormat        string
	RequestID         string  `json:"request_id,omitempty"`
	HeaderModel       string  `json:"header_model,omitempty"`
	Model             string  `json:"model,omitempty"`
	CachedTokensState string  `json:"cached_tokens_state"`
	CachedTokens      *uint64 `json:"cached_tokens,omitempty"`
}

// Identifiers are bounded ASCII tokens, not arbitrary provider strings.
func safeProviderIdentifier(value string) string {
	if len(value) == 0 || len(value) > 256 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' || c == '/') {
			return ""
		}
	}
	return value
}

func providerHeaderEvidence(header http.Header) providerResponseEvidence {
	return providerResponseEvidence{
		RequestID:         safeProviderIdentifier(header.Get("x-request-id")),
		HeaderModel:       safeProviderIdentifier(header.Get("openai-model")),
		CachedTokensState: "unavailable",
	}
}

func (record *captureRecord) observeProviderEvidence(payload []byte) {
	if record.Boundary != "provider" || record.ProviderResponse == nil {
		return
	}
	evidence := record.ProviderResponse
	evidence.CachedTokens = nil
	raw := json.RawMessage(payload)
	for _, key := range []string{"usage", "input_tokens_details", "cached_tokens"} {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			evidence.CachedTokensState = "invalid"
			return
		}
		var present bool
		raw, present = object[key]
		if !present {
			evidence.CachedTokensState = "missing"
			return
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			evidence.CachedTokensState = "null"
			return
		}
	}
	var count uint64
	if json.Unmarshal(raw, &count) != nil {
		evidence.CachedTokensState = "invalid"
		return
	}
	evidence.CachedTokensState = "present"
	evidence.CachedTokens = &count
}

func cloneProviderEvidence(source *providerResponseEvidence) *providerResponseEvidence {
	if source == nil {
		return nil
	}
	result := *source
	if source.CachedTokens != nil {
		count := *source.CachedTokens
		result.CachedTokens = &count
	}
	return &result
}
