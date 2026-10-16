package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type debugOutput struct{ *diagnostic.Bundle }
type debugContextKey struct{}

func openDebugOutput(service *diagnostic.Service, enabled bool, capturePath string) (*debugOutput, error) {
	if !enabled {
		return nil, nil
	}
	if service == nil {
		return nil, errors.New("diagnostics are unavailable")
	}
	root, err := debugStorageDirectory()
	if err != nil {
		return nil, err
	}
	replay, err := ReplayDirectory()
	if err != nil {
		return nil, err
	}
	bundle, err := service.OpenBundle(diagnostic.BundleOptions{Root: root, ReplayDirectory: replay, CapturePath: capturePath, ReadLogPath: os.Getenv(evidence.AXReadOutputEnvironment), Discover: discoverDebugRollouts})
	if err != nil {
		return nil, err
	}
	return &debugOutput{bundle}, nil
}

func (d *debugOutput) handler(next http.Handler) http.Handler {
	if d == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.ObserveThread(codexThreadID(r.Header))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), debugContextKey{}, d)))
	})
}

func (d *debugOutput) instructions(body, wire []byte, headers http.Header, sessionID, requestID string, cachedInput int) {
	if d == nil {
		return
	}
	d.ObserveThread(codexThreadID(headers))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		d.Fail(err)
		return
	}
	// Keep the local projection and the instruction-bearing wire subset distinct.
	// A reconstructed prefix is not itself evidence of provider delivery.
	var input []json.RawMessage
	_ = json.Unmarshal(fields["input"], &input)
	developers := []json.RawMessage{}
	additional := []json.RawMessage{}
	for _, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if responses.StringField(item, "role") == "developer" {
			developers = append(developers, raw)
		}
		if responses.StringField(item, "type") == "additional_tools" {
			additional = append(additional, raw)
		}
	}
	var wireFields map[string]json.RawMessage
	var wireInput []json.RawMessage
	_ = json.Unmarshal(wire, &wireFields)
	_ = json.Unmarshal(wireFields["input"], &wireInput)
	wireDevelopers := []json.RawMessage{}
	wireAdditional := []json.RawMessage{}
	for _, raw := range wireInput {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
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
		"thread_id":         codexThreadID(headers), "session_id": sessionID,
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

func debugRequest(ctx context.Context) (*debugOutput, string) {
	d, _ := ctx.Value(debugContextKey{}).(*debugOutput)
	if d == nil {
		return nil, ""
	}
	if captureID, _ := transportcapture.RequestCorrelation(ctx); captureID != "" {
		return d, captureID
	}
	return d, rand.Text()
}
