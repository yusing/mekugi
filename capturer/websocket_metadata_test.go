package capturer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSocketMetadataResponseEvidence(t *testing.T) {
	recorder := diagnosticRecorder(t)
	request := []byte(`{"type":"response.create","model":"requested","input":[]}`)
	index := 0
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index++
		var headers http.Header
		if index == 1 {
			headers = http.Header{"X-Request-Id": {"handshake-id"}, "Openai-Model": {"handshake-model"}}
		}
		attempt := BeginWebSocketAttempt(r.Context(), request, nil, headers)
		id, model := "current-one", "model-one"
		if index == 2 {
			id, model = "current-two", "model-two"
		}
		message, _ := json.Marshal(map[string]any{"type": "codex.response.metadata", "headers": map[string]string{"X-Request-ID": id, "OpenAI-Model": model, "Authorization": "private credential", "x-codex-turn-state": "private routing"}})
		attempt.Message(message)
		attempt.Message([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
		attempt.Finish(nil)
		_, _ = w.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"requested","input":[]}`)))
	}
	snapshot := recorder.snapshot()
	for index, exchange := range snapshot.Exchanges {
		evidence := exchange.ProviderAttempts[0].ProviderResponse
		if evidence.RequestID != []string{"current-one", "current-two"}[index] || evidence.HeaderModel != []string{"model-one", "model-two"}[index] {
			t.Fatalf("current metadata lost to handshake/missing evidence: %+v", evidence)
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private credential") || strings.Contains(string(encoded), "private routing") {
		t.Fatal("arbitrary metadata headers retained")
	}
	for _, boundary := range []string{"provider", "codex"} {
		record := captureRecord{Boundary: boundary, ProviderResponse: &providerResponseEvidence{}}
		payload := []byte(`{"type":"codex.response.metadata","status":"completed","headers":{"x-request-id":"unsafe identifier","openai-model":"` + strings.Repeat("x", 257) + `"}}`)
		_, terminal := observeResponseJSON(payload, &record)
		if terminal || record.ResponseStatus != "" || record.ProviderResponse.RequestID != "" || record.ProviderResponse.HeaderModel != "" {
			t.Fatalf("unsafe/ancillary metadata became valid terminal evidence: %+v", record)
		}
	}
}
