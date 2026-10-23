package responses

// Source: internal/router/client.go:234:265 and client_websocket.go:329:390@49f759d6 request shaping.

// Wire JSON retains the existing v1 acceptance and exact encoding semantics.
import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strings"
)

const (
	ThreadIDHeader         = "thread-id"
	ClientRequestIDHeader  = "x-client-request-id"
	WindowIDHeader         = "x-codex-window-id"
	TurnMetadataHeader     = "x-codex-turn-metadata"
	BetaFeaturesHeader     = "x-codex-beta-features"
	ResponsesLiteHeader    = "x-openai-internal-codex-responses-lite"
	SubagentHeader         = "x-openai-subagent"
	GuardianHeader         = "x-codex-guardian"
	SessionIDHeader        = "Session_id"
	responsesWebSocketBeta = "responses_websockets=2026-02-06"
)

// ForwardIdentityHeaders preserves the host's client identity without adding defaults.
func ForwardIdentityHeaders(destination, source http.Header) {
	for _, name := range []string{"Originator", "User-Agent"} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func ForwardHeaders(destination, source http.Header) {
	ForwardIdentityHeaders(destination, source)
	for _, name := range []string{
		ThreadIDHeader,
		ClientRequestIDHeader,
		WindowIDHeader,
		BetaFeaturesHeader,
		ResponsesLiteHeader,
		SubagentHeader,
		GuardianHeader,
		TurnMetadataHeader,
		// Codex owns this provider-issued, per-turn sticky-routing token. Relay
		// it unchanged; never derive it from or retain it with the session key.
		"x-codex-turn-state",
	} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func ValidCacheKey(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if (char < 0x20 && char != '\t') || char == 0x7f {
			return false
		}
	}
	return true
}

// Source: codex-rs/core/src/client.rs:824:840,1237:1262,1789:1796 and
// codex-rs/core/src/responses_metadata.rs:307:353 in the read-only Codex clone.
// Turn metadata travels in response.create, not a sticky handshake header.
func WebSocketRequest(body []byte, headers http.Header, cacheKey string) ([]byte, http.Header, bool, error) {
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(body, &fields, jsonv1.DefaultOptionsV1()); err != nil || fields == nil {
		return nil, nil, false, errors.New("invalid provider request")
	}
	var stream bool
	if value := fields["stream"]; value != nil {
		if err := json.Unmarshal(value, &stream, jsonv1.DefaultOptionsV1()); err != nil {
			return nil, nil, false, err
		}
	}
	delete(fields, "stream")
	// Full-input mode never relies on a previous connection's response cache.
	if value := fields["previous_response_id"]; len(value) != 0 && string(value) != "null" {
		return nil, nil, false, errors.New("provider websocket full-input mode does not accept previous_response_id")
	}
	delete(fields, "previous_response_id")
	fields["type"] = jsontext.Value(`"response.create"`)
	metadata := make(map[string]jsontext.Value)
	if value := fields["client_metadata"]; len(value) != 0 && string(value) != "null" {
		if err := json.Unmarshal(value, &metadata, jsonv1.DefaultOptionsV1()); err != nil || metadata == nil {
			return nil, nil, false, errors.New("invalid provider client_metadata")
		}
	}
	handshake := headers.Clone()
	for _, key := range []string{TurnMetadataHeader, "x-codex-turn-state"} {
		if value := headers.Get(key); value != "" {
			if _, present := metadata[key]; !present || key == "x-codex-turn-state" {
				metadata[key], _ = json.Marshal(&value, jsonv1.DefaultOptionsV1())
			}
		}
		handshake.Del(key)
	}
	for _, key := range []string{ThreadIDHeader, WindowIDHeader, SubagentHeader} {
		if value := headers.Get(key); value != "" {
			if _, exists := metadata[key]; !exists {
				metadata[key], _ = json.Marshal(&value, jsonv1.DefaultOptionsV1())
			}
		}
	}
	if ValidCacheKey(cacheKey) {
		handshake[SessionIDHeader] = []string{cacheKey}
	}
	// Match Codex's stable handshake identity; request correlation stays in the
	// capturer's context and does not force a new connection on every turn.
	handshake.Del(ClientRequestIDHeader)
	if thread := headers.Get(ThreadIDHeader); thread != "" {
		handshake.Set(ClientRequestIDHeader, thread)
	}
	handshake.Del("Content-Type")
	handshake.Del("Accept")
	handshake.Set("OpenAI-Beta", responsesWebSocketBeta)
	if value := headers.Get(ResponsesLiteHeader); value != "" {
		metadata["ws_request_header_x_openai_internal_codex_responses_lite"], _ = json.Marshal(&value, jsonv1.DefaultOptionsV1())
	}
	if len(metadata) != 0 {
		fields["client_metadata"], _ = json.Marshal(&metadata, jsonv1.DefaultOptionsV1())
	}
	payload, err := json.Marshal(&fields, jsonv1.DefaultOptionsV1())
	return payload, handshake, stream, err
}
