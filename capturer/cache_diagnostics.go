package capturer

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"

	"slices"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

const maxFingerprintItems = 128

// Fingerprints are HMACs with an ephemeral recorder key, not public content
// hashes. They cannot be compared across recorder lifetimes or reversed using
// a dictionary of candidate prompts. No raw content or routing key is retained.
type requestFingerprint struct {
	Scope       string            `json:"scope"`
	Fields      map[string]string `json:"fields"`
	InputKind   string            `json:"input_kind"`
	Incremental bool              `json:"incremental,omitempty"`
	Items       []string          `json:"items"`
	ItemCount   int               `json:"item_count"`
	Complete    bool              `json:"complete"`
	RequestKey  string            `json:"request_key,omitempty"`
	RoutingKey  string            `json:"routing_key,omitempty"`
	// Nil means unobserved (older evidence or the body-only native seam).
	// An observed empty value means no nonempty turn-state header was sent.
	TurnState *string `json:"turn_state,omitempty"`
}

type prefixComparison struct {
	Status        string   `json:"status"`
	CommonItems   int      `json:"common_items"`
	ChangedFields []string `json:"changed_fields"`
}

type cacheDiagnosis struct {
	PreviousSequence    uint64           `json:"previous_sequence"`
	Client              prefixComparison `json:"client"`
	Native              prefixComparison `json:"native"`
	Provider            prefixComparison `json:"provider"`
	Routing             string           `json:"routing"`
	RequestKey          string           `json:"request_key"`
	TurnStateForwarding string           `json:"turn_state_forwarding,omitempty"`
}

func (r *Recorder) fingerprint(domain string, value any) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	// Values come only from a successful JSON decoder or fixed scalar metadata.
	if encoder.Encode(value) != nil {
		return ""
	}
	hash := hmac.New(sha256.New, r.fingerprintKey[:])
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	hash.Write(encoded.Bytes())
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func normalizeFingerprintNumbers(value any) any {
	switch value := value.(type) {
	case json.Number:
		return json.Number(normalizedNumber(string(value)))
	case map[string]any:
		for key, item := range value {
			value[key] = normalizeFingerprintNumbers(item)
		}
		return value
	case []any:
		for index, item := range value {
			value[index] = normalizeFingerprintNumbers(item)
		}
		return value
	default:
		return value
	}
}

func (r *Recorder) requestFingerprint(body []byte) *requestFingerprint {
	if !json.Valid(body) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var fields map[string]any
	if decoder.Decode(&fields) != nil || fields == nil {
		return nil
	}
	// These fields describe delivery/routing, not cacheable inference input.
	// Normalize the established HTTP and WebSocket representations alike.
	delete(fields, "stream")
	// A continuation transmits a suffix, not the effective model history.
	previousResponse, _ := fields["previous_response_id"].(string)
	delete(fields, "previous_response_id")
	delete(fields, "generate")

	if fields["type"] == responseevents.Create {
		delete(fields, "type")
	}
	if metadata, ok := fields["client_metadata"].(map[string]any); ok {
		for _, key := range []string{"x-codex-turn-state", "x-codex-turn-metadata", "thread-id", "x-codex-window-id", "x-openai-subagent", "ws_request_header_x_openai_internal_codex_responses_lite"} {
			delete(metadata, key)
		}
		if len(metadata) == 0 {
			delete(fields, "client_metadata")
		}
	}
	normalizeFingerprintNumbers(fields)
	fp := &requestFingerprint{Scope: r.fingerprint("scope", nil), Fields: make(map[string]string), Items: []string{}, Complete: true, Incremental: previousResponse != ""}
	if key, present := fields["prompt_cache_key"]; present {
		fp.RequestKey = r.fingerprint("cache-key", key)
	}
	delete(fields, "prompt_cache_key")
	input, hasInput := fields["input"]
	delete(fields, "input")
	for _, key := range []string{"model", "instructions", "tools", "reasoning", "tool_choice", "parallel_tool_calls"} {
		if value, present := fields[key]; present {
			fp.Fields[key] = r.fingerprint("field:"+key, value)
			delete(fields, key)
		}
	}
	fp.Fields["other"] = r.fingerprint("other-fields", fields)
	var items []any
	switch value := input.(type) {
	case []any:
		fp.InputKind = "array"
		items = value
	case string:
		fp.InputKind = "string"
		items = []any{value}
	default:
		if hasInput {
			fp.InputKind = "other"
			items = []any{value}
		} else {
			fp.InputKind = "absent"
		}
	}
	fp.ItemCount = len(items)
	if len(items) > maxFingerprintItems {
		fp.Complete = false
		items = items[:maxFingerprintItems]
	}
	for _, item := range items {
		fp.Items = append(fp.Items, r.fingerprint("input-item", item))
	}
	return fp
}

func cloneFingerprint(source *requestFingerprint) *requestFingerprint {
	if source == nil {
		return nil
	}
	result := *source
	result.Fields = maps.Clone(source.Fields)
	result.Items = slices.Clone(source.Items)
	if source.TurnState != nil {
		value := *source.TurnState
		result.TurnState = &value
	}
	return &result
}

func (r *Recorder) turnStateFingerprint(header string) string {
	if header == "" {
		return ""
	}
	return r.fingerprint("turn-state", header)
}

// Compare the current client/provider headers, not two requests' session keys.
// Turn state legitimately appears after the first response and resets per turn.
func compareTurnState(client, provider *requestFingerprint) string {
	if client == nil || provider == nil || client.Scope != provider.Scope || client.TurnState == nil || provider.TurnState == nil {
		return "unavailable"
	}
	left, right := *client.TurnState, *provider.TurnState
	switch {
	case left == "" && right == "":
		return "absent"
	case left == right:
		return "preserved"
	case right == "":
		return "dropped"
	default:
		return "changed"
	}
}

// This compares decoded request representations, not the provider's private
// tokenized prefix or cache residency. A stable result cannot guarantee a hit.
func comparePrefix(previous, current *requestFingerprint) prefixComparison {
	result := prefixComparison{Status: "unavailable", ChangedFields: []string{}}
	if previous == nil || current == nil || !previous.Complete || !current.Complete || previous.Scope != current.Scope {
		return result
	}
	for _, key := range []string{"model", "instructions", "tools", "reasoning", "tool_choice", "parallel_tool_calls", "other"} {
		if previous.Fields[key] != current.Fields[key] {
			result.ChangedFields = append(result.ChangedFields, key)
		}
	}
	if previous.Incremental || current.Incremental {
		// Settings remain comparable, but neither suffix proves history changes.
		return result
	}
	for result.CommonItems < min(len(previous.Items), len(current.Items)) && previous.Items[result.CommonItems] == current.Items[result.CommonItems] {
		result.CommonItems++
	}
	switch {
	case previous.InputKind != current.InputKind || len(result.ChangedFields) > 0 || result.CommonItems < len(previous.Items):
		result.Status = "changed"
	case len(previous.Items) == len(current.Items):
		result.Status = "identical"
	default:
		result.Status = "appended"
	}
	return result
}

func compareRouting(previous, current *requestFingerprint, body bool) string {
	if previous == nil || current == nil || previous.Scope != current.Scope {
		return "unavailable"
	}
	left, right := previous.RoutingKey, current.RoutingKey
	if body {
		left, right = previous.RequestKey, current.RequestKey
	}
	if left == "" && right == "" {
		return "absent"
	}
	if left == right {
		return "stable"
	}
	return "changed"
}

// Snapshot-local comparisons use arrival order and the preceding logical
// request's final attempt, never completion order or an unrelated thread.
func diagnoseCacheExchanges(exchanges []exchangeMetrics) {
	bySequence := make(map[uint64]int, len(exchanges))
	for index, exchange := range exchanges {
		bySequence[exchange.Sequence] = index
	}
	for index := range exchanges {
		current := &exchanges[index]
		if len(current.ProviderAttempts) == 0 {
			continue
		}
		final := current.ProviderAttempts[len(current.ProviderAttempts)-1]
		if final.Fingerprint == nil {
			continue
		}
		hasTurnState := final.Fingerprint.TurnState != nil || (current.ClientFingerprint != nil && current.ClientFingerprint.TurnState != nil)
		if current.ThreadID == "" && !hasTurnState {
			continue
		}
		empty := prefixComparison{Status: "unavailable", ChangedFields: []string{}}
		diagnosis := &cacheDiagnosis{Client: empty, Native: empty, Provider: empty, Routing: "unavailable", RequestKey: "unavailable"}
		if hasTurnState {
			diagnosis.TurnStateForwarding = compareTurnState(current.ClientFingerprint, final.Fingerprint)
		}
		prior, found := bySequence[current.PredecessorSequence]
		if found && current.ThreadID != "" && current.PredecessorSequence < current.Sequence {
			before := exchanges[prior]
			if before.ThreadID == current.ThreadID && before.Status == "completed" && len(before.ProviderAttempts) > 0 {
				last := before.ProviderAttempts[len(before.ProviderAttempts)-1]
				diagnosis.PreviousSequence = before.Sequence
				diagnosis.Client = comparePrefix(before.ClientFingerprint, current.ClientFingerprint)
				diagnosis.Native = comparePrefix(last.NativeFingerprint, final.NativeFingerprint)
				diagnosis.Provider = comparePrefix(last.Fingerprint, final.Fingerprint)
				diagnosis.Routing = compareRouting(last.Fingerprint, final.Fingerprint, false)
				diagnosis.RequestKey = compareRouting(last.Fingerprint, final.Fingerprint, true)
			}
		}
		current.CacheDiagnosis = diagnosis
	}
}
