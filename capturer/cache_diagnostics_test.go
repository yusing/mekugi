package capturer

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func diagnosticRecorder(t *testing.T) *Recorder {
	t.Helper()
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestCacheFingerprintsArePrivateAndFramingIndependent(t *testing.T) {
	r := diagnosticRecorder(t)
	a := r.requestFingerprint([]byte(`{"model":"model","instructions":"private <instructions>","prompt_cache_key":"private key","input":[{"content":"private <text>","n":1000}]}`))
	b := r.requestFingerprint([]byte(` { "input" : [ { "n":1e3, "content":"private \u003ctext\u003e" } ], "prompt_cache_key":"private key", "instructions":"private \u003cinstructions\u003e", "model":"model" } `))
	if got := comparePrefix(a, b); got.Status != "identical" {
		t.Fatalf("framing changed fingerprint: %+v", got)
	}
	if a.RequestKey != b.RequestKey {
		t.Fatal("stable request cache key changed")
	}
	encoded, _ := json.Marshal(a)
	for _, secret := range []string{"private", "<text>", "<instructions>"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("raw content retained in fingerprint")
		}
	}
	other := diagnosticRecorder(t).requestFingerprint([]byte(`{"model":"model","input":[]}`))
	if got := comparePrefix(a, other); got.Status != "unavailable" {
		t.Fatal("fingerprints compared across recorder lifetimes")
	}
	clone := cloneFingerprint(a)
	clone.Items[0] = "mutated"
	clone.Fields["model"] = "mutated"
	if a.Items[0] == clone.Items[0] || a.Fields["model"] == clone.Fields["model"] {
		t.Fatal("snapshot aliases recorder fingerprint state")
	}
}

func TestCacheFingerprintLocatesChangesAndBoundsHistory(t *testing.T) {
	r := diagnosticRecorder(t)
	base := r.requestFingerprint([]byte(`{"model":"model","input":["one","two"]}`))
	appended := r.requestFingerprint([]byte(`{"input":["one","two","three"],"model":"model"}`))
	if got := comparePrefix(base, appended); got.Status != "appended" || got.CommonItems != 2 {
		t.Fatalf("append: %+v", got)
	}
	changed := r.requestFingerprint([]byte(`{"model":"different","input":["one","changed"]}`))
	if got := comparePrefix(base, changed); got.Status != "changed" || got.CommonItems != 1 || len(got.ChangedFields) != 1 || got.ChangedFields[0] != "model" {
		t.Fatalf("change: %+v", got)
	}
	shortened := r.requestFingerprint([]byte(`{"model":"model","input":["one"]}`))
	if got := comparePrefix(base, shortened); got.Status != "changed" {
		t.Fatalf("removed history: %+v", got)
	}
	items := make([]string, maxFingerprintItems+1)
	encoded, _ := json.Marshal(map[string]any{"input": items})
	truncated := r.requestFingerprint(encoded)
	if truncated.Complete || len(truncated.Items) != maxFingerprintItems || truncated.ItemCount != len(items) {
		t.Fatal("unbounded fingerprint history")
	}
	if comparePrefix(truncated, truncated).Status != "unavailable" {
		t.Fatal("partial fingerprint claimed a stable prefix")
	}
}

func TestCacheDiagnosisUsesArrivalOrderThreadAndFinalAttempt(t *testing.T) {
	r := diagnosticRecorder(t)
	first := r.requestFingerprint([]byte(`{"model":"model","input":["one"]}`))
	first.RoutingKey = "route-a"
	second := r.requestFingerprint([]byte(`{"model":"model","input":["one","two"]}`))
	second.RoutingKey = "route-b"
	exchanges := []exchangeMetrics{
		{Sequence: 2, PredecessorSequence: 1, ThreadID: "thread", Status: "completed", ClientFingerprint: second, ProviderAttempts: []providerAttemptMetrics{{Fingerprint: first}, {Fingerprint: second, ProjectedFingerprint: second}}},
		{Sequence: 1, ThreadID: "thread", Status: "completed", ClientFingerprint: first, ProviderAttempts: []providerAttemptMetrics{{Fingerprint: first, ProjectedFingerprint: first}}},
		{Sequence: 3, ThreadID: "other", Status: "completed", ClientFingerprint: second, ProviderAttempts: []providerAttemptMetrics{{Fingerprint: second, ProjectedFingerprint: second}}},
	}
	diagnoseCacheExchanges(exchanges)
	got := exchanges[0].CacheDiagnosis
	if got.PreviousSequence != 1 || got.Provider.Status != "appended" || got.Projected.Status != "appended" || got.Routing != "changed" {
		t.Fatalf("diagnosis: %+v", got)
	}
	if exchanges[2].CacheDiagnosis.PreviousSequence != 0 || exchanges[2].CacheDiagnosis.Provider.Status != "unavailable" {
		t.Fatal("unrelated thread became cache predecessor")
	}
}

func TestCacheDiagnosisDoesNotSkipPendingPredecessor(t *testing.T) {
	r := diagnosticRecorder(t)
	fp := r.requestFingerprint([]byte(`{"input":["stable"]}`))
	exchanges := []exchangeMetrics{
		{Sequence: 1, ThreadID: "thread", Status: "completed", ProviderAttempts: []providerAttemptMetrics{{Fingerprint: fp}}},
		{Sequence: 3, PredecessorSequence: 2, ThreadID: "thread", Status: "completed", ProviderAttempts: []providerAttemptMetrics{{Fingerprint: fp}}},
	}
	diagnoseCacheExchanges(exchanges)
	if got := exchanges[1].CacheDiagnosis; got.PreviousSequence != 0 || got.Provider.Status != "unavailable" {
		t.Fatalf("skipped pending predecessor: %+v", got)
	}
}

func TestCacheDiagnosisDoesNotBridgeEvictedFailedPredecessor(t *testing.T) {
	r := diagnosticRecorder(t)
	header := http.Header{"Thread-Id": []string{"thread"}}
	first, _ := r.beginRequest(header)
	failed, _ := r.beginRequest(header)
	third, _ := r.beginRequest(header)
	if failed.predecessorSequence != first.sequence || third.predecessorSequence != failed.sequence {
		t.Fatal("arrival predecessor was not retained")
	}
	fp := r.requestFingerprint([]byte(`{"input":["stable"]}`))
	// The failed middle completion has left the bounded detail window before
	// the first and third requests complete. Its identity must still block reuse.
	exchanges := []exchangeMetrics{
		{Sequence: third.sequence, PredecessorSequence: third.predecessorSequence, ThreadID: "thread", Status: "completed", ProviderAttempts: []providerAttemptMetrics{{Fingerprint: fp}}},
		{Sequence: first.sequence, ThreadID: "thread", Status: "completed", ProviderAttempts: []providerAttemptMetrics{{Fingerprint: fp}}},
	}
	diagnoseCacheExchanges(exchanges)
	if got := exchanges[0].CacheDiagnosis; got.PreviousSequence != 0 || got.Provider.Status != "unavailable" {
		t.Fatalf("bridged evicted failed predecessor: %+v", got)
	}
}

func TestTurnStateFingerprintForwarding(t *testing.T) {
	r := diagnosticRecorder(t)
	for _, test := range []struct{ client, provider, want string }{
		{"", "", "absent"}, {"private-state", "private-state", "preserved"},
		{"private-state", "", "dropped"}, {"private-state", "different-state", "changed"},
		{"", "injected-state", "changed"},
	} {
		t.Run(test.want+test.provider, func(t *testing.T) {
			transport := r.Transport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("x-codex-turn-state") != test.provider {
					t.Error("capture changed forwarded header")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[]}`))}, nil
			}))
			handler := r.Handler(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
				body, _ := io.ReadAll(incoming.Body)
				req, _ := http.NewRequestWithContext(incoming.Context(), http.MethodPost, "http://provider/responses", bytes.NewReader(body))
				if test.provider != "" {
					req.Header.Set("x-codex-turn-state", test.provider)
				}
				response, err := transport.RoundTrip(req)
				if err != nil {
					t.Error(err)
					return
				}
				defer response.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.Copy(w, response.Body)
			}))
			incoming := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":[]}`))
			// A current-request comparison must also work without thread identity.
			if test.want != "dropped" {
				incoming.Header.Set("thread-id", "thread")
			}
			if test.client != "" {
				incoming.Header.Set("x-codex-turn-state", test.client)
			}
			handler.ServeHTTP(httptest.NewRecorder(), incoming)
			snapshot := r.snapshot()
			latest := snapshot.Exchanges[len(snapshot.Exchanges)-1]
			if latest.CacheDiagnosis.TurnStateForwarding != test.want {
				t.Fatalf("forwarding status: %s, want %s", latest.CacheDiagnosis.TurnStateForwarding, test.want)
			}
			encoded, _ := json.Marshal(snapshot)
			for _, secret := range []string{"private-state", "different-state", "injected-state"} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatal("raw turn state retained")
				}
			}
			clone := cloneFingerprint(latest.ClientFingerprint)
			*clone.TurnState = "mutated"
			if *latest.ClientFingerprint.TurnState == "mutated" {
				t.Fatal("aliased turn-state fingerprint")
			}
		})
	}
	if compareTurnState(nil, nil) != "unavailable" {
		t.Fatal("missing evidence is not unavailable")
	}
	first := r.requestFingerprint([]byte(`{}`))
	if compareTurnState(first, first) != "unavailable" {
		t.Fatal("legacy evidence is not unavailable")
	}
}

func TestCacheFingerprintIncrementalHistoryIsNotAChangedPrefix(t *testing.T) {
	r := diagnosticRecorder(t)
	full := r.requestFingerprint([]byte(`{"model":"model","input":["base"]}`))
	first := r.requestFingerprint([]byte(`{"type":"response.create","model":"model","previous_response_id":"first-private-id","input":["suffix"]}`))
	next := r.requestFingerprint([]byte(`{"type":"response.create","model":"model","previous_response_id":"second-private-id","input":["different suffix"]}`))
	for _, pair := range [][2]*requestFingerprint{{full, first}, {first, next}, {next, full}} {
		got := comparePrefix(pair[0], pair[1])
		if got.Status != "unavailable" || got.CommonItems != 0 || len(got.ChangedFields) != 0 {
			t.Fatalf("wire suffix claimed a changed prefix: %+v", got)
		}
	}
	if !cloneFingerprint(next).Incremental {
		t.Fatal("clone lost incremental marker")
	}
	changed := r.requestFingerprint([]byte(`{"model":"different","tools":[{"name":"new"}],"previous_response_id":"third","input":[]}`))
	got := comparePrefix(next, changed)
	if got.Status != "unavailable" || len(got.ChangedFields) != 2 || got.ChangedFields[0] != "model" || got.ChangedFields[1] != "tools" {
		t.Fatalf("actual setting changes were hidden: %+v", got)
	}
	warm := r.requestFingerprint([]byte(`{"model":"model","generate":false,"input":["base"]}`))
	if got := comparePrefix(warm, full); got.Status != "identical" {
		t.Fatalf("prewarm transport flag changed inference fingerprint: %+v", got)
	}
	encoded, err := json.Marshal(next)
	if err != nil || strings.Contains(string(encoded), "private-id") {
		t.Fatalf("continuation identity leaked: %s, %v", encoded, err)
	}
}

func TestCacheAttributionLabelsItsEstimate(t *testing.T) {
	snapshot := newMetricsSnapshot("mekugi")
	if snapshot.Cache.AttributionBasis != "previous_input_length_estimate" {
		t.Fatal("cache attribution is not explicitly labeled as an estimate")
	}
}
