package capturer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResponsesWebSocketCapturesLogicalExchanges(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := New(Config{Output: capturePath, Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	firstRequest := []byte(`{"type":"response.create","model":"model","input":[]}`)
	firstResponse := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	clientControl := []byte(`{"type":"response.steer","previous_response_id":"response-1","input":"private steering"}`)
	providerControl := []byte(`{"type":"response.steer","previous_response_id":"response-1","input":"private steering"}`)
	providerAccepted := []byte(`{"type":"response.steer.accepted","response_id":"response-1"}`)
	clientAccepted := []byte(`{"type":"response.steer.accepted","response_id":"response-1"}`)
	successorResponse := []byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`)
	handler := recorder.Handler(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		firstCtx, first := BeginResponsesWebSocket(request.Context(), request.Header, firstRequest)
		if first == nil {
			t.Fatal("GET handler did not expose the WebSocket capture factory")
		}
		firstProvider := BeginWebSocketAttempt(firstCtx, firstRequest, nil, nil)
		firstProvider.Message(firstResponse)
		firstProvider.Finish(nil)
		first.Message(firstResponse)
		first.Finish(nil)
		first.Message([]byte(`{"type":"response.output_text.delta","delta":"late"}`))
		first.Finish(nil)

		ObserveResponsesWebSocketControl(request.Context(), ResponsesWebSocketControlCodex, ResponsesWebSocketControlRequest, clientControl)
		ObserveResponsesWebSocketControl(request.Context(), ResponsesWebSocketControlProvider, ResponsesWebSocketControlRequest, providerControl)
		ObserveResponsesWebSocketControl(request.Context(), ResponsesWebSocketControlProvider, ResponsesWebSocketControlResponse, providerAccepted)
		ObserveResponsesWebSocketControl(request.Context(), ResponsesWebSocketControlCodex, ResponsesWebSocketControlResponse, clientAccepted)

		successorCtx, successor := BeginResponsesWebSocket(request.Context(), request.Header, nil)
		if successor == nil {
			t.Fatal("automatic successor did not receive a capture observer")
		}
		successorProvider := BeginWebSocketAttempt(successorCtx, nil, nil, nil)
		successorProvider.Message(successorResponse)
		successorProvider.Finish(nil)
		successor.Message(successorResponse)
		successor.Finish(nil)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	request.Header.Set("thread-id", "thread")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private steering")) {
		t.Fatal("control capture retained private payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var records []captureRecord
	for {
		var record captureRecord
		if err := decoder.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 8 {
		t.Fatalf("records = %#v, want two provider, two Codex, and four control records", records)
	}
	var logical, controls []captureRecord
	for _, record := range records {
		if record.Boundary == "codex_control" || record.Boundary == "provider_control" {
			controls = append(controls, record)
		} else {
			logical = append(logical, record)
		}
	}
	if len(logical) != 4 || len(controls) != 4 {
		t.Fatalf("logical records = %#v, controls = %#v", logical, controls)
	}
	firstProvider, firstCodex, successorProvider, successorCodex := logical[0], logical[1], logical[2], logical[3]
	if firstProvider.Boundary != "provider" || firstCodex.Boundary != "codex" ||
		successorProvider.Boundary != "provider" || successorCodex.Boundary != "codex" {
		t.Fatalf("boundaries = %q %q %q %q", firstProvider.Boundary, firstCodex.Boundary, successorProvider.Boundary, successorCodex.Boundary)
	}
	if firstCodex.Transport != "websocket" || successorCodex.Transport != "websocket" ||
		firstCodex.StatusCode != http.StatusSwitchingProtocols || successorCodex.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("Codex WebSocket transport lost: first=%+v successor=%+v", firstCodex, successorCodex)
	}
	if firstCodex.Request.Bytes != uint64(len(firstRequest)) || firstCodex.Response.Bytes != uint64(len(firstResponse)) {
		t.Fatalf("first downstream byte accounting = request %d response %d", firstCodex.Request.Bytes, firstCodex.Response.Bytes)
	}
	if successorProvider.Request.Bytes != 0 {
		t.Fatalf("automatic successor fabricated provider request bytes: %d", successorProvider.Request.Bytes)
	}
	if successorCodex.Request.Bytes != 0 || successorCodex.Response.Bytes != uint64(len(successorResponse)) {
		t.Fatalf("automatic successor fabricated or lost bytes: request %d response %d", successorCodex.Request.Bytes, successorCodex.Response.Bytes)
	}
	if !firstCodex.ResponseComplete || firstCodex.ResponseStatus != "completed" ||
		!successorCodex.ResponseComplete || successorCodex.ResponseStatus != "completed" {
		t.Fatalf("terminal status lost: first=%+v successor=%+v", firstCodex, successorCodex)
	}
	if firstCodex.RequestSequence != 1 || successorCodex.RequestSequence != 2 || successorCodex.PredecessorSequence != 1 {
		t.Fatalf("logical sequence attribution = first %d successor %d predecessor %d", firstCodex.RequestSequence, successorCodex.RequestSequence, successorCodex.PredecessorSequence)
	}

	snapshot := recorder.snapshot()
	if snapshot.Requests.Logical != 2 || snapshot.Requests.ProviderAttempts != 2 ||
		snapshot.Requests.Completed != 2 || snapshot.Capture.Records != 8 ||
		snapshot.Capture.MissingProvider != 0 || snapshot.Capture.AttemptGaps != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	clientControlMetric, _ := recorder.measure(clientControl)
	providerControlMetric, _ := recorder.measure(providerControl)
	providerAcceptedMetric, _ := recorder.measure(providerAccepted)
	clientAcceptedMetric, _ := recorder.measure(clientAccepted)
	if snapshot.Transport.ClientControlRequests != payloadTotals(clientControlMetric) ||
		snapshot.Transport.ProviderControlRequests != payloadTotals(providerControlMetric) ||
		snapshot.Transport.ProviderControlResponses != payloadTotals(providerAcceptedMetric) ||
		snapshot.Transport.ClientControlResponses != payloadTotals(clientAcceptedMetric) {
		t.Fatalf("control transport accounting = %+v", snapshot.Transport)
	}
	rebuilt, err := metricsFromRecords(recorder.mode, records)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Requests != snapshot.Requests ||
		rebuilt.Transport.ClientControlRequests != snapshot.Transport.ClientControlRequests ||
		rebuilt.Transport.ProviderControlRequests != snapshot.Transport.ProviderControlRequests ||
		rebuilt.Transport.ProviderControlResponses != snapshot.Transport.ProviderControlResponses ||
		rebuilt.Transport.ClientControlResponses != snapshot.Transport.ClientControlResponses {
		t.Fatalf("control records do not reconcile: live=%+v rebuilt=%+v", snapshot.Transport, rebuilt.Transport)
	}
}

func TestResponsesWebSocketHandshakeDoesNotCreateLogicalRequest(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	called := false
	handler := recorder.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	snapshot := recorder.snapshot()
	if !called || snapshot.Requests.Logical != 0 || snapshot.Capture.Records != 0 {
		t.Fatalf("handshake called = %t, snapshot = %+v", called, snapshot)
	}
}
func TestBeginResponsesWebSocketIsNoOpOutsideRecorderHandler(t *testing.T) {
	ctx := t.Context()
	scoped, exchange := BeginResponsesWebSocket(ctx, nil, []byte(`{"type":"response.create"}`))
	if scoped != ctx || exchange != nil {
		t.Fatalf("outside recorder handler = (%v, %v), want original context and nil observer", scoped, exchange)
	}
}
