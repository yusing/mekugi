package capturer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderDistinguishesNoGenerateFromMissingProvider(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := New(Config{Output: capturePath, Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	terminal := []byte(`{"status":"completed","output":[]}`)
	observedTerminal := observedPayload{content: terminal, bytes: uint64(len(terminal))}
	recordClient := func(body []byte, withProvider bool) {
		t.Helper()
		state, err := recorder.beginRequest(nil)
		if err != nil {
			t.Fatal(err)
		}
		if withProvider {
			ObserveProjectedRequest(context.WithValue(t.Context(), captureKey{}, state), body)
			attempt := state.beginProviderAttempt()
			recorder.recordExchange(state, "provider", attempt, time.Now(), body, observedTerminal, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
		}
		recorder.recordExchange(state, "codex", 0, time.Now(), body, observedTerminal, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	}

	noGenerate := []byte(`{"model":"grok","generate":false,"input":[]}`)
	recordClient(noGenerate, false)
	recordClient([]byte(`{"model":"grok","input":[]}`), false)
	recordClient(noGenerate, true)

	snapshot := recorder.snapshot()
	if snapshot.Requests.Logical != 3 || snapshot.Requests.ProviderAttempts != 1 ||
		snapshot.Requests.Completed != 3 || snapshot.Capture.MissingProvider != 1 {
		t.Fatalf("provider expectation metrics = %+v", snapshot)
	}

	file, err := os.Open(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
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
	if len(records) != 4 {
		t.Fatalf("records = %#v", records)
	}
	first, ordinary, provider, third := records[0], records[1], records[2], records[3]
	if first.ProviderExpected == nil || *first.ProviderExpected ||
		ordinary.ProviderExpected != nil ||
		provider.Boundary != "provider" || provider.ProviderAttempt != 1 ||
		third.ProviderExpected == nil || *third.ProviderExpected {
		t.Fatalf("provider expectation evidence = first %#v ordinary %#v provider %#v third %#v", first.ProviderExpected, ordinary.ProviderExpected, provider, third.ProviderExpected)
	}
	rebuilt, err := metricsFromRecords(recorder.mode, records)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Requests != snapshot.Requests || rebuilt.Capture.MissingProvider != snapshot.Capture.MissingProvider {
		t.Fatalf("provider expectation does not reconcile: live=%+v rebuilt=%+v", snapshot, rebuilt)
	}
}

func TestRecorderMarksMissingProjectedRequestIncomplete(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	state, err := recorder.beginRequest(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"model":"test","input":[]}`)
	terminal := []byte(`{"status":"completed","output":[]}`)
	response := observedPayload{content: terminal, bytes: uint64(len(terminal))}
	attempt := state.beginProviderAttempt()
	recorder.recordExchange(state, "provider", attempt, time.Now(), request, response, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	recorder.recordExchange(state, "codex", 0, time.Now(), request, response, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	snapshot := recorder.snapshot()
	if snapshot.Capture.CaptureErrors != 1 || len(snapshot.Exchanges) != 1 ||
		len(snapshot.Exchanges[0].ProviderAttempts) != 1 || snapshot.Exchanges[0].ProviderAttempts[0].ProjectedRequest != nil {
		t.Fatalf("missing projected request was presented as complete: %+v", snapshot)
	}
}
