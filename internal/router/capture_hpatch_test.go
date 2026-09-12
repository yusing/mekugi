package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestCaptureHPatchCarrierOutcomes(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			recorder, err := capturer.New(capturer.Config{Mode: "mekugi", ModelProtocol: "native"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recorder.Close() })
			var providerItems, deliveredItems []any
			for i, history := range []mekugiHistory{
				{translationError: "type: command 1, reason row-stale: private detail"},
				{patch: "private patch", report: "private report", changeID: "c1"},
				{patch: "private patch", report: "private report", changeID: "c2"},
				{patch: "private patch", report: "private report"},
			} {
				name := "hpatch"
				if i == 1 {
					name = "hpatch_recover"
				}
				id := "call-" + strconv.Itoa(i)
				providerItems = append(providerItems, map[string]any{"type": "custom_tool_call", "call_id": id, "name": name, "input": "private edit"})
				deliveredItems = append(deliveredItems, map[string]any{"type": "custom_tool_call", "call_id": id, "name": "exec", "input": history.carrierInput()})
			}
			writeResponse := func(w http.ResponseWriter, items []any) {
				terminal := map[string]any{"status": "completed", "output": items}
				if !streaming {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustTestJSON(t, terminal))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for i, item := range items {
					_, _ = w.Write(append(append([]byte("data: "), mustTestJSON(t, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})...), '\n', '\n'))
				}
				terminal["output"] = []any{}
				_, _ = w.Write(append(append([]byte("data: "), mustTestJSON(t, map[string]any{"type": "response.completed", "response": terminal})...), '\n', '\n'))
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeResponse(w, providerItems)
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
				_, err = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				writeResponse(w, deliveredItems)
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)))
			var output bytes.Buffer
			if err := recorder.WriteMetrics(&output); err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				Mekugi struct {
					Calls, Corrections, Successful, Rejected, Unmatched uint64
					Diagnostics                                         map[string]uint64
				}
			}
			if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			got := snapshot.Mekugi
			if got.Calls != 4 || got.Corrections != 1 || got.Successful != 3 || got.Rejected != 1 || got.Unmatched != 0 || got.Diagnostics["row-stale"] != 1 {
				t.Fatalf("HPATCH outcomes = %+v", got)
			}
			if strings.Contains(output.String(), "private") {
				t.Fatal("metrics retained carrier content")
			}
		})
	}
}
