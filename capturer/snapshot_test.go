package capturer

import (
	"bytes"
	"encoding/json/v2"
	"reflect"
	"testing"
)

func TestSnapshotDetachedAndMatchesWriteMetrics(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recorder.Close() })
	// Seed every mutable exported detail through its serialized boundary. This
	// isolates copy ownership from provider parsing and request accounting tests.
	err = json.Unmarshal([]byte(`{
  "schema":"mekugi.capture.metrics.v6","mode":"mekugi",
  "cache":{"provider_cache_rate":0.25,"eligible_prefix_cache_rate":0.5},
  "provider_tools":{"exec_command":{"calls":2}},
  "delivered_tools":{"exec_command":{"calls":1}},
  "exchanges":[{"sequence":1,"thread_id":"thread","usage":{"input_tokens":12},
    "compaction_summary_bytes":30,"compaction_changes":2,"compaction_failures":1,
    "journal":{"operations":{"log":2},"last_outcome_empty":false},
    "delivered_tools":[{"name":"exec_command","call_id":"call-1"}],
    "provider_attempts":[{"attempt":1,"usage":{"input_tokens":12},
      "projected_request":{"bytes":40,"tokens":4},
      "provider_response":{"cached_tokens_state":"present","cached_tokens":0},
      "tools":[{"name":"exec_command","call_id":"call-1"}]}]}]}`), &recorder.metrics, json.RejectUnknownMembers(true))
	if err != nil {
		t.Fatal(err)
	}
	var baseline bytes.Buffer
	if err := recorder.WriteMetrics(&baseline); err != nil {
		t.Fatal(err)
	}
	// The expected value must not share pointers with either the recorder or
	// Snapshot: otherwise a shallow copy could mutate both sides of the check.
	var want MetricsSnapshot
	if err := json.Unmarshal(baseline.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	checkExport := func() {
		t.Helper()
		var encoded bytes.Buffer
		if err := recorder.WriteMetrics(&encoded); err != nil {
			t.Fatal(err)
		}
		var decoded MetricsSnapshot
		if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("WriteMetrics differs from detached snapshot:\ngot  %+v\nwant %+v", decoded, want)
		}
	}
	checkExport()
	for _, tc := range []struct {
		name   string
		mutate func(*MetricsSnapshot)
	}{
		{"provider-tools-map", func(s *MetricsSnapshot) { delete(s.ProviderTools, "exec_command") }},
		{"delivered-tools-map", func(s *MetricsSnapshot) { delete(s.DeliveredTools, "exec_command") }},
		{"provider-cache-rate", func(s *MetricsSnapshot) { *s.Cache.ProviderCacheRate = 99 }},
		{"eligible-cache-rate", func(s *MetricsSnapshot) { *s.Cache.EligiblePrefixCacheRate = 99 }},
		{"exchange-slice", func(s *MetricsSnapshot) { s.Exchanges[0].ThreadID = "mutated" }},
		{"exchange-usage", func(s *MetricsSnapshot) { s.Exchanges[0].Usage.InputTokens = 99 }},
		{"compaction-summary", func(s *MetricsSnapshot) { *s.Exchanges[0].CompactionSummaryBytes = 99 }},
		{"compaction-changes", func(s *MetricsSnapshot) { *s.Exchanges[0].CompactionChanges = 99 }},
		{"compaction-failures", func(s *MetricsSnapshot) { *s.Exchanges[0].CompactionFailures = 99 }},
		{"delivered-tools-slice", func(s *MetricsSnapshot) { s.Exchanges[0].DeliveredTools[0].Name = "mutated" }},
		{"journal-map", func(s *MetricsSnapshot) { s.Exchanges[0].Journal.Operations["log"] = 99 }},
		{"journal-optional", func(s *MetricsSnapshot) { *s.Exchanges[0].Journal.LastOutcomeEmpty = true }},
		{"attempt-slice", func(s *MetricsSnapshot) { s.Exchanges[0].ProviderAttempts[0].Attempt = 99 }},
		{"attempt-usage", func(s *MetricsSnapshot) { s.Exchanges[0].ProviderAttempts[0].Usage.InputTokens = 99 }},
		{"projected-request", func(s *MetricsSnapshot) { s.Exchanges[0].ProviderAttempts[0].ProjectedRequest.Bytes = 99 }},
		{"provider-evidence", func(s *MetricsSnapshot) { s.Exchanges[0].ProviderAttempts[0].ProviderResponse.Model = "mutated" }},
		{"explicit-cached-telemetry", func(s *MetricsSnapshot) { *s.Exchanges[0].ProviderAttempts[0].ProviderResponse.CachedTokens = 99 }},
		{"attempt-tools-slice", func(s *MetricsSnapshot) { s.Exchanges[0].ProviderAttempts[0].Tools[0].Name = "mutated" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := json.Unmarshal(baseline.Bytes(), &recorder.metrics); err != nil {
				t.Fatal(err)
			}
			snapshot := recorder.Snapshot()
			tc.mutate(&snapshot)
			if got := recorder.Snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("mutating %s leaked into recorder", tc.name)
			}
			checkExport()
		})
	}
}
