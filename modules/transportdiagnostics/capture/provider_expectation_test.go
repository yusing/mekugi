package capture

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecorderDistinguishesNoGenerateFromMissingProvider(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := new(Service).Open(Config{Output: capturePath, Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	terminal := []byte(`{"status":"completed","output":[]}`)
	observedTerminal := observedPayload{content: terminal, bytes: uint64(len(terminal))}
	recordClient := func(body []byte, withProvider, local bool) {
		t.Helper()
		state := recorder.beginRequest(nil)
		ctx := context.WithValue(t.Context(), captureKey{}, state)
		if local {
			ObserveLocalResponse(ctx)
		}
		if withProvider {
			ObserveProjectedRequest(ctx, body)
			attempt := state.beginProviderAttempt()
			recorder.recordExchange(state, "provider", attempt, time.Now(), body, observedTerminal, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
		}
		recorder.recordExchange(state, "codex", 0, time.Now(), body, observedTerminal, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
	}

	noGenerate := []byte(`{"model":"grok","generate":false,"input":[]}`)
	ordinaryBody := []byte(`{"model":"grok","input":[]}`)
	recordClient(noGenerate, false, false)
	recordClient(ordinaryBody, false, false)
	recordClient(noGenerate, true, false)
	recordClient(ordinaryBody, false, true)
	recordClient(ordinaryBody, true, true)

	snapshot := recorder.snapshot()
	if snapshot.Requests.Logical != 5 || snapshot.Requests.ProviderAttempts != 2 ||
		snapshot.Requests.Completed != 5 || snapshot.Capture.MissingProvider != 1 {
		t.Fatalf("provider expectation metrics = %+v", snapshot)
	}
	for _, exchange := range snapshot.Exchanges {
		if exchange.ProviderAttempts == nil {
			t.Fatal("provider attempts must serialize as an array, including missing evidence")
		}
	}

	file, err := os.Open(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := jsontext.NewDecoder(file)
	var records []captureRecord
	for {
		var record captureRecord
		if err := json.UnmarshalDecode(decoder, &record, jsonv1.DefaultOptionsV1()); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 7 {
		t.Fatalf("records = %#v", records)
	}
	first, ordinary, provider, third := records[0], records[1], records[2], records[3]
	if first.ProviderExpected == nil || *first.ProviderExpected ||
		ordinary.ProviderExpected != nil ||
		provider.Boundary != "provider" || provider.ProviderAttempt != 1 ||
		third.ProviderExpected == nil || *third.ProviderExpected {
		t.Fatalf("provider expectation evidence = first %#v ordinary %#v provider %#v third %#v", first.ProviderExpected, ordinary.ProviderExpected, provider, third.ProviderExpected)
	}
	for _, index := range []int{4, 6} {
		if record := records[index]; record.ProviderExpected == nil || *record.ProviderExpected {
			t.Fatalf("local response lost its provider decision: %+v", record)
		}
	}
	rebuilt, err := metricsFromRecords(recorder.mode, records)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Requests != snapshot.Requests || rebuilt.Capture.MissingProvider != snapshot.Capture.MissingProvider {
		t.Fatalf("provider expectation does not reconcile: live=%+v rebuilt=%+v", snapshot, rebuilt)
	}
}

func TestRecorderProjectedRequestObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		observe bool
		body    []byte
	}{
		{name: "missing"},
		{name: "empty automatic successor", observe: true},
		{name: "valid", observe: true, body: []byte(`{"model":"test","input":["projected"]}`)},
		{name: "invalid fingerprint", observe: true, body: []byte(`{`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, err := new(Service).Open(Config{Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			defer recorder.Close()
			state := recorder.beginRequest(nil)
			if tc.observe {
				ObserveProjectedRequest(context.WithValue(t.Context(), captureKey{}, state), tc.body)
			}
			request := []byte(`{"model":"test","input":[]}`)
			terminal := []byte(`{"status":"completed","output":[]}`)
			response := observedPayload{content: terminal, bytes: uint64(len(terminal))}
			attempt := state.beginProviderAttempt()
			recorder.recordExchange(state, "provider", attempt, time.Now(), request, response, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
			recorder.recordExchange(state, "codex", 0, time.Now(), request, response, http.StatusOK, "application/json", "", nil, providerResponseEvidence{})
			snapshot := recorder.snapshot()
			if len(snapshot.Exchanges) != 1 || len(snapshot.Exchanges[0].ProviderAttempts) != 1 {
				t.Fatalf("missing exchange or attempt: %+v", snapshot)
			}
			got := snapshot.Exchanges[0].ProviderAttempts[0]
			wantError := ""
			var wantErrors uint64
			if !tc.observe {
				wantError = "missing projected request observation"
				wantErrors = 1
			}
			if snapshot.Capture.CaptureErrors != wantErrors || got.CaptureError != wantError {
				t.Fatalf("observation health = %+v, attempt = %+v", snapshot.Capture, got)
			}
			if !reflect.DeepEqual(got.ProjectedFingerprint, recorder.requestFingerprint(tc.body)) {
				t.Fatalf("projected fingerprint = %+v", got.ProjectedFingerprint)
			}
		})
	}
}
