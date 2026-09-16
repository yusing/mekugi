package capturer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectProviderUsage(t *testing.T) {
	at := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	record := captureRecord{SchemaVersion: schemaVersion, Boundary: "provider", CaptureID: "one", ProviderAttempt: 1,
		ThreadID: "thread", RequestModel: "gpt-6-astra", CapturedAt: at, ResponseComplete: true,
		Usage: &ProviderUsage{EvidenceComplete: new(true), InputTokens: 100, CachedTokens: 80, OutputTokens: 20, ReasoningTokens: 5}}
	filter := UsageInspectionFilter{Since: at.Add(-time.Hour), Until: at.Add(time.Hour), Model: "gpt-*", Threads: map[string]bool{"thread": true}}
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	write := func(records ...captureRecord) {
		t.Helper()
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		enc := json.NewEncoder(file)
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	missing := record
	missing.CaptureID = "two"
	missing.Usage = nil
	write(record, missing)
	result, err := InspectProviderUsage(t.Context(), path, filter)
	if err != nil || result["thread"].State != "incomplete" || result["thread"].MissingRecords != 1 ||
		result["thread"].Tokens.InputTokens != 100 {
		t.Fatalf("%+v %v", result, err)
	}
	legacy := record
	legacy.Usage = &ProviderUsage{InputTokens: 100, CachedTokens: 80, OutputTokens: 20, ReasoningTokens: 5}
	write(legacy)
	unknown, err := InspectProviderUsage(t.Context(), path, filter)
	if err != nil || unknown["thread"].State != "incomplete" || unknown["thread"].UnknownCompletenessRecords != 1 || unknown["thread"].Tokens != nil {
		t.Fatalf("legacy normalization became complete evidence: %+v %v", unknown, err)
	}
	write(record, record)
	if _, err := InspectProviderUsage(t.Context(), path, filter); err == nil {
		t.Fatal("accepted duplicate provider attempt")
	}
	write(record)
	filter.ExcludeModel = "*astra"
	result, err = InspectProviderUsage(t.Context(), path, filter)
	if err != nil || len(result) != 0 {
		t.Fatalf("model filtering: %+v %v", result, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := InspectProviderUsage(cancelled, path, filter); err == nil {
		t.Fatal("ignored cancellation")
	}
	bad := record
	bad.Usage = &ProviderUsage{EvidenceComplete: new(true), InputTokens: 1, CachedTokens: 2}
	write(bad)
	filter.ExcludeModel = ""
	if _, err := InspectProviderUsage(t.Context(), path, filter); err == nil {
		t.Fatal("accepted invalid usage")
	}
}
