package capture

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderCompactionMetricsExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := new(Service).Open(Config{Output: path, Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	state := recorder.beginRequest(http.Header{})
	ctx := context.WithValue(t.Context(), captureKey{}, state)
	ObserveRequestKind(ctx, "compaction")
	request := []byte(`{"model":"model","input":"private-request-marker"}`)
	ObserveProjectedRequest(ctx, request)
	const summary = "private-summary-marker 界"
	response := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + summary + `"}]}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":20},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2}}}`)
	payload := observedPayload{content: response, bytes: uint64(len(response))}
	attempt := state.beginProviderAttempt()
	ObserveProviderUsage(ctx, ProviderUsage{InputTokens: 100, CachedTokens: 20, OutputTokens: 7, ReasoningTokens: 2})
	recorder.recordExchange(state, "provider", attempt, time.Now(), request, payload, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	recorder.recordExchange(state, "codex", 0, time.Now(), request, payload, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	var exported bytes.Buffer
	if err := recorder.WriteMetrics(&exported); err != nil {
		t.Fatal(err)
	}
	var snapshot MetricsSnapshot
	if err := json.Unmarshal(exported.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Exchanges) != 1 {
		t.Fatalf("exchanges = %d", len(snapshot.Exchanges))
	}
	exchange := snapshot.Exchanges[0]
	if exchange.RequestKind != "compaction" {
		t.Fatalf("kind = %q", exchange.RequestKind)
	}
	if snapshot.Capture.MissingProvider != 0 {
		t.Fatalf("missing provider = %d", snapshot.Capture.MissingProvider)
	}
	if len(exchange.ProviderAttempts) != 1 || snapshot.Requests.ProviderAttempts != 1 {
		t.Fatalf("provider attempts = %+v", exchange.ProviderAttempts)
	}
	want := usageMetrics{InputTokens: 100, CachedInputTokens: 20, UncachedInputTokens: 80, OutputTokens: 7, ReasoningTokens: 2, ProviderAttempts: 1, UnknownAttempts: 1}
	if exchange.Usage == nil || *exchange.Usage != want || snapshot.Usage != want {
		t.Fatalf("provider usage lost: exchange=%+v total=%+v", exchange.Usage, snapshot.Usage)
	}
	records, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(records), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("records = %d, want %d", len(lines), 2)
	}
	for _, line := range lines {
		var record captureRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.RequestKind != "compaction" || record.CaptureError != "" {
			t.Fatalf("record = %+v", record)
		}
	}
	for _, data := range [][]byte{records, exported.Bytes()} {
		for _, private := range []string{"private-request-marker", "private-summary-marker"} {
			if bytes.Contains(data, []byte(private)) {
				t.Fatalf("sanitized metrics retained %q", private)
			}
		}
	}
}
