package capturer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sessionExport(t *testing.T, thread string) string {
	t.Helper()
	directory := t.TempDir()
	recorder, err := New(Config{Output: filepath.Join(directory, "capture.jsonl"), Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		state := &requestState{captureID: fmt.Sprintf("%s-%d", thread, sequence), sequence: sequence, threadID: thread}
		recorder.cacheQueues[thread] = append(recorder.cacheQueues[thread], state)
		record := captureRecord{SchemaVersion: schemaVersion, Mode: "mekugi", ModelProtocol: "native", CaptureID: state.captureID, RequestSequence: sequence, PredecessorSequence: sequence - 1, ThreadID: thread, ResponseStatus: "completed", ResponseComplete: true, StatusCode: 200}
		record.Boundary = "provider"
		record.ProviderAttempt = 1
		record.Usage = &ProviderUsage{InputTokens: 100, CachedTokens: 40, OutputTokens: 10}
		state.addProvider(record)
		recorder.write(record, state)
		record.Boundary = "codex"
		record.ProviderAttempt = 0
		record.Usage = nil
		recorder.write(record, state)
	}
	var snapshot bytes.Buffer
	if err := recorder.WriteMetrics(&snapshot); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), snapshot.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestMergeSessionsUsesLiveCaptureCalculationsAndRebasesIdentities(t *testing.T) {
	directories := []string{sessionExport(t, "one"), sessionExport(t, "two")}
	original, err := os.ReadFile(filepath.Join(directories[0], "capture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var metrics, capture bytes.Buffer
	if err := MergeSessions(directories, &metrics, &capture); err != nil {
		t.Fatal(err)
	}
	var snapshot metricsSnapshot
	if err := json.Unmarshal(metrics.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Requests.Logical != 4 || snapshot.Usage.InputTokens != 400 || snapshot.Capture.Records != 8 || snapshot.Cache.ProviderCacheRate == nil || *snapshot.Cache.ProviderCacheRate != 0.4 {
		t.Fatalf("merged metrics: %+v", snapshot)
	}
	if snapshot.Exchanges[2].Sequence != 3 || snapshot.Exchanges[2].PredecessorSequence != 0 || snapshot.Exchanges[3].PredecessorSequence != 3 {
		t.Fatal("session sequence identities crossed")
	}
	current, _ := os.ReadFile(filepath.Join(directories[0], "capture.jsonl"))
	if !bytes.Equal(original, current) {
		t.Fatal("original evidence modified")
	}
}

func TestMergeSessionsRejectsMissingTamperedAndDuplicateEvidence(t *testing.T) {
	directory := sessionExport(t, "one")
	var output bytes.Buffer
	if err := MergeSessions([]string{directory, directory}, &output, &output); err == nil {
		t.Fatal("duplicate session accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "metrics.json"), []byte(`{"schema":"mekugi.capture.metrics.v4","mode":"mekugi","model_protocol":"native"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MergeSessions([]string{directory}, &output, &output); err == nil {
		t.Fatal("tampered metrics accepted")
	}
	if err := MergeSessions(nil, &output, &output); err == nil {
		t.Fatal("empty input accepted")
	}
	if output.Len() != 0 {
		t.Fatal("failed validation emitted partial evidence")
	}
}

func TestMergeSessionsAcceptsLegacyCacheAttribution(t *testing.T) {
	directory := sessionExport(t, "legacy")
	path := filepath.Join(directory, "metrics.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"attribution_basis":"previous_input_length_estimate",`), nil, 1)
	if bytes.Contains(data, []byte(`attribution_basis`)) {
		t.Fatal("legacy fixture retained new field")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var metrics, capture bytes.Buffer
	if err := MergeSessions([]string{directory}, &metrics, &capture); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(metrics.Bytes(), []byte(`"attribution_basis":"previous_input_length_estimate"`)) {
		t.Fatal("merged legacy evidence did not label the estimate")
	}
}
