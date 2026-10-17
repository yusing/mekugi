// Source: apps/mekugi/internal/router/debug.go:1:128@b9679c93.
package requestexecution

import (
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type Debug struct{ *diagnostic.Bundle }
type debugContextKey struct{}

func (d *Debug) Handler(next http.Handler) http.Handler {
	if d == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.ObserveThread(codexresponses.ThreadID(r.Header))
		next.ServeHTTP(w, r.WithContext(WithDebug(r.Context(), d)))
	})
}

func (d *Debug) instructions(body, wire []byte, headers http.Header, sessionID, requestID string, cachedInput int) {
	if d == nil {
		return
	}
	d.ObserveThread(codexresponses.ThreadID(headers))
	var fields map[string]jsonv1.RawMessage
	if err := json.Unmarshal(body, &fields, jsonv1.DefaultOptionsV1()); err != nil {
		d.Fail(err)
		return
	}
	// Keep the local projection and the instruction-bearing wire subset distinct.
	// A reconstructed prefix is not itself evidence of provider delivery.
	var input []jsonv1.RawMessage
	_ = json.Unmarshal(fields["input"], &input, jsonv1.DefaultOptionsV1())
	developers := []jsonv1.RawMessage{}
	additional := []jsonv1.RawMessage{}
	for _, raw := range input {
		var item map[string]jsonv1.RawMessage
		if json.Unmarshal(raw, &item, jsonv1.DefaultOptionsV1()) != nil {
			continue
		}
		if responses.StringField(item, "role") == "developer" {
			developers = append(developers, raw)
		}
		if responses.StringField(item, "type") == "additional_tools" {
			additional = append(additional, raw)
		}
	}
	var wireFields map[string]jsonv1.RawMessage
	var wireInput []jsonv1.RawMessage
	_ = json.Unmarshal(wire, &wireFields, jsonv1.DefaultOptionsV1())
	_ = json.Unmarshal(wireFields["input"], &wireInput, jsonv1.DefaultOptionsV1())
	wireDevelopers := []jsonv1.RawMessage{}
	wireAdditional := []jsonv1.RawMessage{}
	for _, raw := range wireInput {
		var item map[string]jsonv1.RawMessage
		if json.Unmarshal(raw, &item, jsonv1.DefaultOptionsV1()) != nil {
			continue
		}
		if responses.StringField(item, "role") == "developer" {
			wireDevelopers = append(wireDevelopers, raw)
		}
		if responses.StringField(item, "type") == "additional_tools" {
			wireAdditional = append(wireAdditional, raw)
		}
	}
	d.Instructions(map[string]any{
		"timestamp": time.Now().UTC(), "request_id": requestID,
		"client_request_id": headers.Get("x-client-request-id"),
		"thread_id":         codexresponses.ThreadID(headers), "session_id": sessionID,
		"scope": "projected_responses_request", "cached_input_items": cachedInput,
		"wire_request_present": len(wire) != 0, "wire_input_items": len(wireInput),
		"projected_input_bytes": len(fields["input"]), "wire_input_bytes": len(wireFields["input"]),
		"wire_developer_messages": wireDevelopers, "wire_additional_tools": wireAdditional,
		"wire_previous_response_id": wireFields["previous_response_id"],
		"cache_rebased":             len(wire) != 0 && responses.StringField(fields, "previous_response_id") != "" && responses.StringField(wireFields, "previous_response_id") == "",
		"model":                     fields["model"], "previous_response_id": fields["previous_response_id"],
		"instructions": fields["instructions"], "developer_messages": developers,
		"tools": fields["tools"], "additional_tools": additional,
	})
}

func DebugRequest(ctx context.Context) (*Debug, string) {
	d, _ := ctx.Value(debugContextKey{}).(*Debug)
	if d == nil {
		return nil, ""
	}
	if captureID, _ := transportcapture.RequestCorrelation(ctx); captureID != "" {
		return d, captureID
	}
	return d, rand.Text()
}

func WithDebug(ctx context.Context, debug *Debug) context.Context {
	return context.WithValue(ctx, debugContextKey{}, debug)
}
