package transportdiagnostics_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestModuleRetainsSchemaSevenUsageEvidence(t *testing.T) {
	// Frozen records keep compatibility independent of the current encoder.
	const historical = `{"schema_version":7,"boundary":"provider","capture_id":"complete","provider_attempt":1,"thread_id":"complete-thread","request_model":"gpt-6-astra","captured_at":"2026-09-15T10:00:00Z","response_complete":true,"usage":{"evidence_complete":true,"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_tokens":5}}
{"schema_version":7,"boundary":"provider","capture_id":"legacy","provider_attempt":1,"thread_id":"legacy-thread","request_model":"gpt-6-astra","captured_at":"2026-09-15T10:00:00Z","response_complete":true,"usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_tokens":5}}
{"schema_version":7,"boundary":"provider","capture_id":"zero","provider_attempt":1,"thread_id":"zero-thread","request_model":"gpt-6-astra","captured_at":"2026-09-15T10:00:00Z","response_complete":true,"usage":{"evidence_complete":true,"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"reasoning_tokens":0}}
`
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(path, []byte(historical), 0600); err != nil {
		t.Fatal(err)
	}
	service := moduletest.New(t, transportdiagnostics.Module, transportdiagnostics.Deps{}, module.Slots{}, nil)
	filter := capture.UsageInspectionFilter{
		Since: time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC),
		Model: "gpt-*", Threads: map[string]bool{"complete-thread": true, "legacy-thread": true, "zero-thread": true},
	}
	before, err := service.InspectProviderUsage(t.Context(), path, filter)
	if err != nil {
		t.Fatal(err)
	}
	complete := before["complete-thread"]
	if len(before) != 3 || complete.State != "observed" || complete.ObservedRecords != 1 || complete.MissingRecords != 0 || complete.UnknownCompletenessRecords != 0 || complete.Tokens == nil || complete.Tokens.InputTokens != 100 || complete.Tokens.CachedTokens != 80 || complete.Tokens.OutputTokens != 20 || complete.Tokens.ReasoningTokens != 5 {
		t.Fatalf("complete historical evidence=%+v", complete)
	}
	legacy := before["legacy-thread"]
	if legacy.State != "incomplete" || legacy.ObservedRecords != 0 || legacy.MissingRecords != 1 || legacy.UnknownCompletenessRecords != 1 || legacy.Tokens != nil {
		t.Fatalf("legacy completeness became authoritative usage: %+v", legacy)
	}
	zero := before["zero-thread"]
	if zero.State != "observed" || zero.ObservedRecords != 1 || zero.MissingRecords != 0 || zero.UnknownCompletenessRecords != 0 || zero.Tokens == nil || *zero.Tokens != (capture.ProviderUsage{}) {
		t.Fatalf("explicit zero became missing evidence: %+v", zero)
	}
	// Opening the retained destination must append, without restoring old metrics.
	recorder, err := service.Open(capture.Config{Output: path, Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recorder.Snapshot()
	closeErr := recorder.Close()
	if closeErr != nil || snapshot.Schema != "mekugi.capture.metrics.v7" || snapshot.Requests.Logical != 0 || snapshot.Requests.ProviderAttempts != 0 || snapshot.Usage.InputTokens != 0 {
		t.Fatalf("fresh recorder snapshot=%+v close=%v", snapshot, closeErr)
	}
	reopened := moduletest.New(t, transportdiagnostics.Module, transportdiagnostics.Deps{}, module.Slots{}, nil)
	after, err := reopened.InspectProviderUsage(t.Context(), path, filter)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("reopened usage=%+v error=%v", after, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != historical {
		t.Fatalf("retained records changed: %q, %v", data, err)
	}
}
