package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestCaptureRenderedHpatchOutcomes(t *testing.T) {
	recorder, err := capturer.New(capturer.Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })

	histories := []mekugiHistory{
		{Patch: testTranslatedPatch, Report: changeNotice("amber1") + testMekugiReport, ChangeID: "amber1"},
		{AlreadySatisfied: true, Report: changeNotice("amber2") + testMekugiReport, ChangeID: "amber2"},
		{TranslationError: changeNotice("amber3") + "type: command 2, reason row-stale: private-sentinel\n"},
	}
	var emitted, delivered []any
	for i, history := range histories {
		id := fmt.Sprintf("call-%d", i)
		emitted = append(emitted, map[string]any{"type": "custom_tool_call", "call_id": id, "name": "hpatch", "input": "private-sentinel"})
		delivered = append(delivered, map[string]any{"type": "custom_tool_call", "call_id": id, "name": "exec", "input": history.carrierInput()})
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustTestJSON(t, map[string]any{"status": "completed", "output": emitted}))
	}))
	t.Cleanup(upstream.Close)
	client := &http.Client{Transport: recorder.Transport(http.DefaultTransport)}
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream.URL+"/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("provider response: %v, %v", readErr, closeErr)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustTestJSON(t, map[string]any{"status": "completed", "output": delivered}))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model"}`)))
	var metrics bytes.Buffer
	if err := recorder.WriteMetrics(&metrics); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Mekugi struct {
			Calls        uint64            `json:"calls"`
			Successful   uint64            `json:"successful"`
			Rejected     uint64            `json:"rejected"`
			Unclassified uint64            `json:"unclassified"`
			Unmatched    uint64            `json:"unmatched"`
			Diagnostics  map[string]uint64 `json:"diagnostics"`
		} `json:"mekugi"`
	}
	if err := json.Unmarshal(metrics.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	got := snapshot.Mekugi
	if got.Calls != 3 || got.Successful != 2 || got.Rejected != 1 ||
		got.Unclassified != 0 || got.Unmatched != 0 || got.Diagnostics["row-stale"] != 1 {
		t.Fatalf("rendered carrier classification: %+v", got)
	}
	if bytes.Contains(metrics.Bytes(), []byte("private-sentinel")) {
		t.Fatal("carrier content leaked into metrics")
	}
}
