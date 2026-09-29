package capturer

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestObserveJournalClonesCumulativeSnapshot(t *testing.T) {
	ObserveJournal(t.Context(), &JournalMetrics{}) // No recorder is a no-op.
	state := &requestState{}
	ctx := context.WithValue(t.Context(), captureKey{}, state)
	metrics := &JournalMetrics{StartedAt: "2026-09-29T00:00:00Z", Sequence: 2, Operations: map[string]uint64{"log": 2}, LastOutcomeEmpty: new(false)}
	ObserveJournal(ctx, metrics)
	metrics.Operations["log"] = 99
	*metrics.LastOutcomeEmpty = true
	if state.journal.Operations["log"] != 2 || *state.journal.LastOutcomeEmpty {
		t.Fatalf("observation aliases caller: %+v", state.journal)
	}
	// Repeated observations replace the cumulative snapshot, not add to it.
	want := &JournalMetrics{StartedAt: metrics.StartedAt, Sequence: 3, Operations: map[string]uint64{"log": 3}, FinalAnswers: 1, FinalAnswerBytes: 12, LastOutcomeEmpty: new(false)}
	ObserveJournal(ctx, want)
	ObserveJournal(ctx, want)
	if !reflect.DeepEqual(state.journal, want) {
		t.Fatalf("snapshot = %+v, want %+v", state.journal, want)
	}
}

func TestJournalMetricsExportBoundaryAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := New(Config{Output: path, Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	wants := []*JournalMetrics{
		{StartedAt: "2026-09-29T00:00:00Z", Sequence: 4, Operations: map[string]uint64{"plan": 1, "log": 2}, FinalAnswers: 1, FinalAnswerBytes: 7, LastOutcomeEmpty: new(false)},
		{StartedAt: "2026-09-29T00:00:00Z", Sequence: 5, Operations: map[string]uint64{"plan": 1, "log": 3}, FinalAnswers: 2, FinalAnswerBytes: 7, EmptyOutcomes: 1, LastOutcomeEmpty: new(true)},
		nil,
	}
	request := []byte(`{"model":"model","input":"private-journal-title"}`)
	response := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private-journal-body"}]}]}`)
	for i, want := range wants {
		header := http.Header{}
		thread := "thread-a"
		if i == 2 {
			thread = "thread-b"
		}
		header.Set("thread-id", thread)
		state, err := recorder.beginRequest(header)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(t.Context(), captureKey{}, state)
		ObserveJournal(ctx, want)
		payload := observedPayload{content: response, bytes: uint64(len(response))}
		attempt := state.beginProviderAttempt()
		recorder.recordExchange(state, "provider", attempt, time.Now(), request, payload, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
		recorder.recordExchange(state, "codex", 0, time.Now(), request, payload, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
		if state.journal != nil {
			state.journal.Operations["log"] = 99
			*state.journal.LastOutcomeEmpty = !*state.journal.LastOutcomeEmpty
		}
	}
	check := func(snapshot metricsSnapshot) {
		t.Helper()
		if len(snapshot.Exchanges) != len(wants) {
			t.Fatalf("exchanges = %d", len(snapshot.Exchanges))
		}
		for i, want := range wants {
			if !reflect.DeepEqual(snapshot.Exchanges[i].Journal, want) {
				t.Fatalf("exchange %d journal = %+v, want %+v", i, snapshot.Exchanges[i].Journal, want)
			}
		}
	}
	snapshot := recorder.snapshot()
	check(snapshot)
	snapshot.Exchanges[0].Journal.Operations["plan"] = 99
	*snapshot.Exchanges[0].Journal.LastOutcomeEmpty = true
	check(recorder.snapshot())
	var exported bytes.Buffer
	if err := recorder.WriteMetrics(&exported); err != nil {
		t.Fatal(err)
	}
	var decoded metricsSnapshot
	if err := json.Unmarshal(exported.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	check(decoded)
	records, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(records), []byte("\n"))
	if len(lines) != 6 {
		t.Fatalf("capture records = %d, want 6", len(lines))
	}
	for i, line := range lines {
		var record captureRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		want := wants[i/2]
		if i%2 == 0 {
			want = nil
		}
		if !reflect.DeepEqual(record.Journal, want) {
			t.Fatalf("record %d (%s) journal = %+v, want %+v", i, record.Boundary, record.Journal, want)
		}
		if want == nil && bytes.Contains(line, []byte(`"journal"`)) {
			t.Fatalf("record %d emitted absent journal", i)
		}
	}
	for _, data := range [][]byte{records, exported.Bytes()} {
		for _, private := range []string{"private-journal-title", "private-journal-body"} {
			if bytes.Contains(data, []byte(private)) {
				t.Fatalf("sanitized metrics retained %q", private)
			}
		}
	}
}

func TestJournalMetricsJSONPresence(t *testing.T) {
	metrics := JournalMetrics{StartedAt: "2026-09-29T00:00:00Z", Operations: map[string]uint64{"log": 0}, LastOutcomeEmpty: new(false)}
	data, err := json.Marshal(&metrics)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"started_at": "2026-09-29T00:00:00Z", "sequence": float64(0), "operations": map[string]any{"log": float64(0)},
		"standalone_requests": float64(0), "final_answers": float64(0),
		"final_answer_bytes": float64(0), "empty_outcomes": float64(0), "last_outcome_empty": false,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("JSON fields = %#v, want %#v", fields, want)
	}
	metrics.LastOutcomeEmpty = nil
	data, err = json.Marshal(&metrics)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"last_outcome_empty"`)) {
		t.Fatal("unknown outcome was emitted")
	}
	var absent *JournalMetrics
	if absent.Clone() != nil {
		t.Fatal("nil clone should remain absent")
	}
}
