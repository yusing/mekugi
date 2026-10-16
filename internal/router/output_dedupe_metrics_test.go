package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestDuplicateOutputMeasuredReceiptReachesCapture(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	proxy.inputProjections = testInputProjections()
	recorder, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	body := strings.Repeat("measured consuming output\n", 30)
	repeated := append(duplicateTestInput("first", "a", body), duplicateTestInput("second", "b", body)...)
	var total int64
	for i, input := range [][]any{repeated, repeated[:2], nil} {
		original := responses.MustMarshalJSON(input)
		projection := proxy.inputProjections[0]
		want := projection.Prepare(original)(original).Receipt
		if want == nil || !want.TokensMeasured {
			t.Fatalf("request %d has no measured receipt: %+v", i, want)
		}
		handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{
				"model": "gpt-test", "input": input, "tools": testExecResponsesTools(),
			}))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := proxy.prepareRequest(r.Context(), &request, "session", "thread", codexTurnMetadata{RequestKind: "turn"}, true)
			if err != nil {
				t.Fatal(err)
			}
			transform.Close()
			w.WriteHeader(http.StatusOK)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`)))
		total += want.SavedTokens
		got := recorder.Snapshot().OutputDeduplication
		if got.Projections != uint64(i+1) || got.Occurrences != 1 || got.SavedTokens != total || got.Unmeasured != 0 {
			t.Fatalf("after request %d: metrics = %+v, want savings %d", i, got, total)
		}
	}
}
