package capturer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestKindEvidence(t *testing.T) {
	for _, kind := range []string{"turn", "prewarm", "compaction", "private input", ""} {
		t.Run(kind, func(t *testing.T) {
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
			ObserveRequestKind(ctx, kind)
			want := kind
			if kind == "private input" {
				want = ""
			}
			request := []byte(`{"model":"model"}`)
			response := []byte(`{"status":"completed","output":[]}`)
			payload := observedPayload{content: response, bytes: uint64(len(response))}
			attempt := state.beginProviderAttempt()
			recorder.recordExchange(state, "provider", attempt, time.Now(), request, payload,
				http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
			recorder.recordExchange(state, "codex", 0, time.Now(), request, payload,
				http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
			snapshot := recorder.snapshot()
			if len(snapshot.Exchanges) != 1 || snapshot.Exchanges[0].RequestKind != want {
				t.Fatalf("snapshot request kind = %#v, want %q", snapshot.Exchanges, want)
			}
			records, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(records, []byte("private input")) {
				t.Fatal("unsafe request kind retained")
			}
			lines := bytes.Split(bytes.TrimSpace(records), []byte("\n"))
			if len(lines) != 2 {
				t.Fatalf("records=%d, want 2", len(lines))
			}
			for _, line := range lines {
				var record captureRecord
				if err := json.Unmarshal(line, &record); err != nil {
					t.Fatal(err)
				}
				if record.RequestKind != want {
					t.Fatalf("%s kind=%q, want %q", record.Boundary, record.RequestKind, want)
				}
			}
		})
	}
	ObserveRequestKind(t.Context(), "turn") // No recorder is a no-op.
}
