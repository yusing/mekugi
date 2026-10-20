package responses

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: internal/router/request.go:20:240@06fc40e4 request envelopes.

type Request struct {
	originalBody   []byte
	originalFields map[string]jsontext.Value
	Fields         map[string]jsontext.Value
	Stream         bool
	// CachedInput is the provider-confirmed projected prefix, set before sending.
	CachedInput int
	RebaseInput bool
}

// IncrementalBody removes only the projected prefix already cached upstream.
// Preparation still sees that prefix; HTTP and Chat Completions providers do not use
// the provider's connection-local Responses cache.
func (r Request) IncrementalBody(body []byte, useCache bool) ([]byte, error) {
	if (r.CachedInput == 0 && !r.RebaseInput) || !useCache {
		return body, nil
	}
	var fields map[string]jsontext.Value
	var input []jsontext.Value
	if json.Unmarshal(body, &fields, jsonv1.DefaultOptionsV1()) != nil || json.Unmarshal(fields["input"], &input, jsonv1.DefaultOptionsV1()) != nil || r.CachedInput > len(input) {
		return nil, errors.New("invalid WebSocket cached input boundary")
	}
	fields["input"] = responses.MustMarshalJSON(input[r.CachedInput:])
	if r.RebaseInput {
		delete(fields, "previous_response_id")
	}
	return responses.MarshalJSON(fields)
}

// Model returns the model name from the request.
func (r Request) Model() string {
	raw, ok := r.Fields["model"]
	if !ok {
		return ""
	}
	var model string
	if json.Unmarshal(raw, &model, jsonv1.DefaultOptionsV1()) != nil {
		return ""
	}
	return strings.TrimSpace(model)
}

func (r Request) ReasoningEffort() string {
	var reasoning struct {
		Effort string `json:"effort"`
	}
	if raw, ok := r.Fields["reasoning"]; ok {
		_ = json.Unmarshal(raw, &reasoning, jsonv1.DefaultOptionsV1())
	}
	return strings.TrimSpace(reasoning.Effort)
}

// PromptCacheKey returns the prompt cache key from the request.
func (r Request) PromptCacheKey() string {
	raw, ok := r.Fields["prompt_cache_key"]
	if !ok {
		return ""
	}
	var key string
	if json.Unmarshal(raw, &key, jsonv1.DefaultOptionsV1()) != nil || !ValidCacheKey(key) {
		return ""
	}
	return key
}

// ParseRequest parses and validates a Responses request from raw JSON bytes.
func ParseRequest(body []byte) (Request, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(body), jsonv1.DefaultOptionsV1())
	var request map[string]jsontext.Value
	if err := json.UnmarshalDecode(decoder, &request, jsonv1.DefaultOptionsV1()); err != nil {
		return Request{}, fmt.Errorf("decode Responses request: %w", err)
	}
	if request == nil {
		return Request{}, errors.New("responses request must be a JSON object")
	}
	var trailing any
	if err := json.UnmarshalDecode(decoder, &trailing, jsonv1.DefaultOptionsV1()); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("decode Responses request: multiple JSON values")
		}
		return Request{}, fmt.Errorf("decode Responses request trailing data: %w", err)
	}

	var streamResponse bool
	_ = json.Unmarshal(request["stream"], &streamResponse, jsonv1.DefaultOptionsV1())
	var backgroundResponse bool
	_ = json.Unmarshal(request["background"], &backgroundResponse, jsonv1.DefaultOptionsV1())
	if backgroundResponse {
		return Request{}, errors.New("background Responses requests are not supported")
	}
	return Request{Fields: request, Stream: streamResponse, originalBody: body, originalFields: maps.Clone(request)}, nil
}

// OriginalField returns a read-only value from the received request.
func (r Request) OriginalField(name string) jsontext.Value { return r.originalFields[name] }
