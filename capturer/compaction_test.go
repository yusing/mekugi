package capturer

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/commentaryid"
)

func TestCompactionMetricsExport(t *testing.T) {
	for _, answer := range []string{"router", "provider"} {
		t.Run(answer, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.jsonl")
			recorder, err := New(Config{Output: path, Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			defer recorder.Close()
			state, err := recorder.beginRequest(http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(t.Context(), captureKey{}, state)
			ObserveRequestKind(ctx, "compaction")
			// Provider counts must remain unknown, even if a caller supplies numbers.
			ObserveCompaction(ctx, answer, 123, 4, 0)
			request := []byte(`{"model":"model","input":"private-request-marker"}`)
			ObserveProjectedRequest(ctx, request)
			const summary = "private-summary-marker 界"
			response := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + summary + `"}]}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":20},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":2}}}`)
			if answer == "router" {
				response = []byte(strings.Replace(string(response), `"type":"message"`, `"type":"message","id":"`+commentaryid.CompactionPrefix+`fixture"`, 1))
			}
			payload := observedPayload{content: response, bytes: uint64(len(response))}
			if answer == "provider" {
				attempt := state.beginProviderAttempt()
				ObserveProviderUsage(ctx, ProviderUsage{InputTokens: 100, CachedTokens: 20, OutputTokens: 7, ReasoningTokens: 2})
				recorder.recordExchange(state, "provider", attempt, time.Now(), request, payload, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
			}
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
			check := func(metrics CompactionMetrics) {
				t.Helper()
				if metrics.CompactionAnswer != answer {
					t.Fatalf("answer = %q, want %q", metrics.CompactionAnswer, answer)
				}
				wantBytes := uint64(len(summary))
				if answer == "router" {
					wantBytes = 123
				}
				if metrics.CompactionSummaryBytes == nil || *metrics.CompactionSummaryBytes != wantBytes {
					t.Fatalf("summary bytes = %v, want %d", metrics.CompactionSummaryBytes, wantBytes)
				}
				if answer == "router" {
					if metrics.CompactionChanges == nil || *metrics.CompactionChanges != 4 || metrics.CompactionFailures == nil || *metrics.CompactionFailures != 0 {
						t.Fatalf("router evidence = %+v", metrics)
					}
				} else if metrics.CompactionChanges != nil || metrics.CompactionFailures != nil {
					t.Fatalf("provider evidence should be unknown: %+v", metrics)
				}
			}
			check(exchange.CompactionMetrics)
			if snapshot.Capture.MissingProvider != 0 {
				t.Fatalf("missing provider = %d", snapshot.Capture.MissingProvider)
			}
			wantRecords := 1
			if answer == "router" {
				if exchange.ClientFinalText != (payloadMetrics{}) {
					t.Fatalf("router summary was counted as model text: %+v", exchange.ClientFinalText)
				}
				if len(exchange.ProviderAttempts) != 0 || snapshot.Requests.ProviderAttempts != 0 || snapshot.Usage != (usageMetrics{}) {
					t.Fatalf("fabricated provider evidence: %+v", snapshot)
				}
				if exchange.Usage == nil || *exchange.Usage != (usageMetrics{}) {
					t.Fatalf("router usage is not observed zero: %+v", exchange.Usage)
				}
			} else {
				wantRecords = 2
				if len(exchange.ProviderAttempts) != 1 || snapshot.Requests.ProviderAttempts != 1 {
					t.Fatalf("provider attempts = %+v", exchange.ProviderAttempts)
				}
				want := usageMetrics{InputTokens: 100, CachedInputTokens: 20, UncachedInputTokens: 80, OutputTokens: 7, ReasoningTokens: 2, ProviderAttempts: 1}
				if exchange.Usage == nil || *exchange.Usage != want || snapshot.Usage != want {
					t.Fatalf("provider usage lost: exchange=%+v total=%+v", exchange.Usage, snapshot.Usage)
				}
			}
			records, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(records), []byte("\n"))
			if len(lines) != wantRecords {
				t.Fatalf("records = %d, want %d", len(lines), wantRecords)
			}
			for _, line := range lines {
				var record captureRecord
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				check(record.CompactionMetrics)
				if record.RequestKind != "compaction" || record.CaptureError != "" {
					t.Fatalf("record = %+v", record)
				}
				if answer == "router" && (record.Boundary != "codex" || record.ProviderAttempt != 0 || record.Usage != nil || record.ProviderExpected == nil || *record.ProviderExpected) {
					t.Fatalf("fabricated router provider record: %+v", record)
				}
			}
			for _, data := range [][]byte{records, exported.Bytes()} {
				for _, private := range []string{"private-request-marker", "private-summary-marker"} {
					if bytes.Contains(data, []byte(private)) {
						t.Fatalf("sanitized metrics retained %q", private)
					}
				}
			}
		})
	}
}

func TestObserveCompactionIgnoresInvalidCalls(t *testing.T) {
	for _, tc := range []struct {
		name, kind, answer      string
		size, changes, failures int
	}{
		{name: "turn", kind: "turn", answer: "router"},
		{name: "prewarm", kind: "prewarm", answer: "router"},
		{name: "unknown kind", answer: "provider"},
		{name: "unknown answer", kind: "compaction", answer: "private-answer-marker"},
		{name: "empty answer", kind: "compaction"},
		{name: "negative size", kind: "compaction", answer: "router", size: -1},
		{name: "negative changes", kind: "compaction", answer: "router", changes: -1},
		{name: "negative failures", kind: "compaction", answer: "router", failures: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &requestState{}
			ctx := context.WithValue(t.Context(), captureKey{}, state)
			ObserveRequestKind(ctx, tc.kind)
			ObserveCompaction(ctx, tc.answer, tc.size, tc.changes, tc.failures)
			if state.compaction != (CompactionMetrics{}) {
				t.Fatalf("invalid call retained metrics: %+v", state.compaction)
			}
		})
	}
	ObserveCompaction(t.Context(), "router", 0, 0, 0) // Absent recorder is a no-op.
	state := &requestState{}
	ctx := context.WithValue(t.Context(), captureKey{}, state)
	ObserveRequestKind(ctx, "compaction")
	ObserveCompaction(ctx, "router", 0, 0, 0)
	before := state.compaction
	ObserveCompaction(ctx, "invalid", 1, 1, 1)
	if state.compaction != before {
		t.Fatal("invalid observation replaced valid metrics")
	}
	if before.CompactionSummaryBytes == nil || *before.CompactionSummaryBytes != 0 || before.CompactionChanges == nil || before.CompactionFailures == nil {
		t.Fatalf("known zeros lost: %+v", before)
	}
}
