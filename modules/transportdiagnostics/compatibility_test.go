package transportdiagnostics_test

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
	"github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestModuleRetainsSchemaSevenUsageEvidence(t *testing.T) {
	// Frozen records keep compatibility independent of the current encoder.
	const historical = `{"schema_version":7,"boundary":"provider","capture_id":"complete","provider_attempt":1,"thread_id":"complete-thread","request_model":"gpt-6-astra","captured_at":"2026-09-15T10:00:00Z","response_complete":true,"usage":{"evidence_complete":true,"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_tokens":5}}
{"schema_version":7,"boundary":"provider","capture_id":"incomplete","provider_attempt":1,"thread_id":"incomplete-thread","request_model":"gpt-6-astra","captured_at":"2026-09-15T10:00:00Z","response_complete":true,"usage":{"evidence_complete":false,"input_tokens":100,"cached_input_tokens":80,"output_tokens":20,"reasoning_tokens":5}}
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
		Model: "gpt-*", Threads: map[string]bool{"complete-thread": true, "incomplete-thread": true, "zero-thread": true},
	}
	before, err := service.InspectProviderUsage(t.Context(), path, filter)
	if err != nil {
		t.Fatal(err)
	}
	complete := before["complete-thread"]
	if len(before) != 3 || complete.State != "observed" || complete.ObservedRecords != 1 || complete.MissingRecords != 0 || complete.Tokens == nil || complete.Tokens.InputTokens != 100 || complete.Tokens.CachedTokens != 80 || complete.Tokens.OutputTokens != 20 || complete.Tokens.ReasoningTokens != 5 {
		t.Fatalf("complete historical evidence=%+v", complete)
	}
	incomplete := before["incomplete-thread"]
	if incomplete.State != "incomplete" || incomplete.ObservedRecords != 0 || incomplete.MissingRecords != 1 || incomplete.Tokens != nil {
		t.Fatalf("incomplete evidence became authoritative usage: %+v", incomplete)
	}
	zero := before["zero-thread"]
	if zero.State != "observed" || zero.ObservedRecords != 1 || zero.MissingRecords != 0 || zero.Tokens == nil || *zero.Tokens != (capture.ProviderUsage{EvidenceComplete: true}) {
		t.Fatalf("explicit zero became missing evidence: %+v", zero)
	}
	usage, err := service.InspectUsage(t.Context(), path, inspection.UsageFilter{
		Since: filter.Since, Until: filter.Until, Model: filter.Model, Threads: filter.Threads,
	})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := json.Marshal(usage)
	if err != nil || strings.Contains(string(exported), `"unknown_completeness_records"`) {
		t.Fatalf("inspection completeness export: %s, %v", exported, err)
	}
	for _, id := range []string{"complete-thread", "zero-thread"} {
		var tokens capture.ProviderUsage
		if err := json.Unmarshal(usage[id].Tokens, &tokens); err != nil || !tokens.EvidenceComplete {
			t.Fatalf("complete inspection tokens for %s: %+v, %v", id, tokens, err)
		}
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
