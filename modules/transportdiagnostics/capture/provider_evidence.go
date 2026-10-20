// Source: capturer/provider_evidence.go@b5162407.
package capture

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
)

// providerResponseEvidence contains only allowlisted provider telemetry, never
// arbitrary headers, response text, credentials, or routing tokens.
type providerResponseEvidence struct {
	wireFormat        string
	RequestID         string  `json:"request_id,omitempty"`
	HeaderModel       string  `json:"header_model,omitempty"`
	Model             string  `json:"model,omitempty"`
	ServiceTier       string  `json:"service_tier,omitempty"`
	CachedTokensState string  `json:"cached_tokens_state"`
	CachedTokens      *uint64 `json:"cached_tokens,omitempty"`
}

func providerHeaderEvidence(header http.Header) providerResponseEvidence {
	return providerResponseEvidence{
		RequestID:         safeDiagnosticIdentifier(header.Get("x-request-id")),
		HeaderModel:       safeDiagnosticIdentifier(header.Get("openai-model")),
		CachedTokensState: "unavailable",
	}
}

func providerServiceTier(raw []byte) string {
	var tier string
	if json.Unmarshal(raw, &tier) != nil {
		return ""
	}
	return safeDiagnosticIdentifier(tier)
}

func (record *captureRecord) observeProviderEvidence(payload []byte) {
	if record.Boundary != "provider" || record.ProviderResponse == nil {
		return
	}
	evidence := record.ProviderResponse
	evidence.CachedTokens = nil
	evidence.ServiceTier = ""
	raw := jsonv1.RawMessage(payload)
	for _, key := range []string{"usage", "input_tokens_details", "cached_tokens"} {
		var object map[string]jsonv1.RawMessage
		if json.Unmarshal(raw, &object, jsonv1.DefaultOptionsV1()) != nil || object == nil {
			evidence.CachedTokensState = "invalid"
			return
		}
		if key == "usage" {
			evidence.ServiceTier = providerServiceTier(object["service_tier"])
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
	if json.Unmarshal(raw, &count, jsonv1.DefaultOptionsV1()) != nil {
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
