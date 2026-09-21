package capturer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestWebSocketCaptureUsesActualMessageBytes(t *testing.T) {
	recorder := diagnosticRecorder(t)
	request := []byte(`{"type":"response.create","model":"model","input":[]}`)
	first := []byte("{\n\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg\",\"content\":[{\"type\":\"output_text\",\"text\":\"private response text\"}]}}")
	terminal := []byte(`{"type":"response.completed","response":{"model":"actual-model","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":2},"output_tokens":3}}}`)
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ObserveProjectedRequest(r.Context(), []byte(`{"model":"model","input":[]}`))
		headers := http.Header{}
		headers.Set("Session_id", "session")
		headers.Set("x-codex-turn-state", "private-route")
		observation := BeginWebSocketAttempt(r.Context(), request, headers, http.Header{"X-Request-Id": {"request-first"}})
		observation.Message(first)
		observation.Message(terminal)
		ObserveProviderUsage(r.Context(), ProviderUsage{InputTokens: 10, CachedTokens: 2, OutputTokens: 3})
		observation.Finish(nil)
		observation.Finish(nil)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(append(append([]byte("data: "), terminal...), []byte("\n\n")...))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":[]}`)))
	snapshot := recorder.snapshot()
	attempt := snapshot.Exchanges[0].ProviderAttempts[0]
	if snapshot.Requests.ProviderAttempts != 1 || attempt.Transport != "websocket" || !attempt.ResponseComplete || attempt.Status != "completed" {
		t.Fatalf("bad WS accounting: %+v", attempt)
	}
	if attempt.Request.Bytes != uint64(len(request)) || attempt.Response.Bytes != uint64(len(first)+len(terminal)) {
		t.Fatalf("framing changed byte counts: %+v", attempt)
	}
	firstMetric, _ := recorder.measure(first)
	terminalMetric, _ := recorder.measure(terminal)
	if attempt.Response.Tokens != firstMetric.Tokens+terminalMetric.Tokens || attempt.FinalOutput.Bytes == 0 {
		t.Fatal("JSON event token/output accounting differs")
	}
	if attempt.Usage == nil || attempt.Usage.CachedInputTokens != 2 || attempt.ProviderResponse.RequestID != "request-first" || attempt.ProviderResponse.CachedTokens == nil || *attempt.ProviderResponse.CachedTokens != 2 {
		t.Fatalf("usage/evidence lost: %+v", attempt)
	}
	if snapshot.Capture.CaptureErrors != 0 || snapshot.Capture.Incomplete != 0 {
		t.Fatalf("WS101 classified as HTTP error: %+v", snapshot.Capture)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, secret := range []string{"private response text", "private-route"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("retained %q", secret)
		}
	}
}

func TestWebSocketFingerprintIgnoresOnlyTransportMetadata(t *testing.T) {
	recorder := diagnosticRecorder(t)
	httpBody := []byte(`{"model":"model","stream":true,"input":[{"role":"user","content":"same input"}],"client_metadata":{"custom":"keep"}}`)
	wsBody := []byte(`{"type":"response.create","model":"model","input":[{"role":"user","content":"same input"}],"client_metadata":{"custom":"keep","x-codex-turn-state":"new-state","x-codex-turn-metadata":"new-turn","thread-id":"thread","ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
	a, b := recorder.requestFingerprint(httpBody), recorder.requestFingerprint(wsBody)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("transport perturbed inference fingerprints: %v %v", a, b)
	}
	changed := bytes.Replace(wsBody, []byte(`"custom":"keep"`), []byte(`"custom":"changed"`), 1)
	if reflect.DeepEqual(a, recorder.requestFingerprint(changed)) {
		t.Fatal("arbitrary metadata change hidden")
	}
	changed = bytes.Replace(wsBody, []byte("same input"), []byte("new input"), 1)
	if reflect.DeepEqual(a, recorder.requestFingerprint(changed)) {
		t.Fatal("inference input change hidden")
	}
}

func TestWebSocketCaptureBoundsAndIncomplete(t *testing.T) {
	recorder := diagnosticRecorder(t)
	state, err := recorder.beginRequest(http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), captureKey{}, state)
	observation := BeginWebSocketAttempt(ctx, []byte(`{"type":"response.create","model":"model"}`), nil, nil)
	oversized := []byte(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", maxObservedResponseBytes) + `"}`)
	observation.Message(oversized)
	observation.Finish(nil)
	records := state.providerRecords()
	if len(records) != 1 || records[0].Response.Bytes != uint64(len(oversized)) || records[0].ResponseComplete || records[0].Response.Tokens != 0 || records[0].CaptureError != "response payload exceeds capture observation limit" {
		t.Fatalf("overflow became valid: %+v", records)
	}
}
