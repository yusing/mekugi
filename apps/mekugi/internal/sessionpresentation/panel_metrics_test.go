package sessionpresentation

import (
	"context"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestFeatureMetricsSeparateSelectedThreadFromAllDescendants(t *testing.T) {
	counts := featureCountsFixture{
		"root":      {Spawns: 1, SpawnPreloads: 1},
		"worker":    {Spawns: 1, AllWaits: 1},
		"nested":    {CompletionChanges: 1},
		"unrelated": {SideQuestions: 1},
	}
	read := func(thread string) FeatureSnapshot {
		descendants := map[string][]string{"root": {"worker", "nested"}, "worker": {"nested"}}
		return ReadNativeFeatureUse(t.Context(), thread, descendants[thread], counts)
	}
	snapshot := &transportcapture.MetricsSnapshot{Schema: "mekugi.capture.metrics.v7", OutputDeduplicationThreads: map[string]transportcapture.OutputDeduplicationMetrics{
		"root": {Projections: 1, Occurrences: 1}, "worker": {Projections: 1, Occurrences: 2, SavedTokens: 20}, "nested": {Projections: 1, Occurrences: 4, SavedTokens: 40}, "unrelated": {Projections: 1, Occurrences: 8},
	}}
	got := NewFeatureMetrics("worker", "worker", snapshot, true, read("worker"))
	want := FeatureMetrics{
		Self:        FeatureCounts{FeatureUse: observation.FeatureUse{Spawns: 1, AllWaits: 1}, OutputDeduplication: transportcapture.OutputDeduplicationMetrics{Projections: 1, Occurrences: 2, SavedTokens: 20}},
		Descendants: FeatureCounts{FeatureUse: observation.FeatureUse{CompletionChanges: 1}, OutputDeduplication: transportcapture.OutputDeduplicationMetrics{Projections: 1, Occurrences: 4, SavedTokens: 40}},
		Thread:      "worker", Agents: 1,
	}
	if *got != want {
		t.Fatalf("worker features = %+v", *got)
	}
	counts["root"] = observation.FeatureUse{Spawns: 1, SpawnPreloads: 1, ProgressWaits: 1, ProgressYields: 1, AssignmentSources: 1, MessageSources: 1, SelfCompactionsCompleted: 1, SelfCompactionFailures: 1}
	counts["nested"] = observation.FeatureUse{CompletionChanges: 1, ProgressWaits: 2, ProgressYields: 2, AssignmentSources: 2, MessageSources: 2, SelfCompactionsCompleted: 2, SelfCompactionFailures: 2}
	root := NewFeatureMetrics("root", "Main", snapshot, true, read("root"))
	if root.Thread != "Main" || root.Agents != 2 || root.Self.SpawnPreloads != 1 || root.Descendants.AllWaits != 1 || root.Descendants.CompletionChanges != 1 || root.Descendants.OutputDeduplication.Occurrences != 6 || root.Descendants.SideQuestions != 0 {
		t.Fatalf("root features = %+v", *root)
	}
	cells := func(features *FeatureMetrics) map[[2]string]string {
		values := make(map[[2]string]string)
		for _, field := range MetricFields(&PanelView{Metrics: snapshot, Features: features}, metricFeatures, 0) {
			if len(field.Values) > 0 {
				values[[2]string{field.Group, field.Label}] = strings.Join(field.Values, "/")
			} else {
				values[[2]string{field.Group, field.Label}] = field.Value
			}
		}
		return values
	}
	values := cells(root)
	for key, want := range map[[2]string]string{
		{"Compaction", ""}: "Self/Descendants", {"Spawns", "Total"}: "1/1", {"Spawns", "With preload"}: "1/0",
		{"Waits", "Progress enabled"}: "1/2", {"Waits", "Progress delivered"}: "1/2",
		{"Compaction", "Self completed"}: "1/2", {"Compaction", "Self failed"}: "1/2",
		{"Source-bearing sends", "Total"}: "2/4", {"Source-bearing sends", "Assignments"}: "1/2", {"Source-bearing sends", "Messages"}: "1/2",
		{"Waits", "All mode"}: "0/1", {"Delegate finals", "With edit ranges"}: "0/1", {"Output deduplication", "Saved tokens (est.)"}: "0/60",
	} {
		if values[key] != want {
			t.Fatalf("%v = %q, want %q", key, values[key], want)
		}
	}
	if !strings.Contains(values[[2]string{"Features", ""}], "Self is Main; Descendants sums its admitted agents (2)") {
		t.Fatalf("features note = %q", values[[2]string{"Features", ""}])
	}
	// A disabled feature reads off, never as zero receipts.
	off := cells(NewFeatureMetrics("root", "Main", snapshot, false, read("root")))
	if _, counted := off[[2]string{"Output deduplication", "Occurrences"}]; counted || off[[2]string{"Output deduplication", ""}] != "Off for this launch." {
		t.Fatalf("disabled deduplication = %v", off)
	}
}

// Snapshot fixture, not a second feature counter producer.
type featureCountsFixture map[string]observation.FeatureUse

func (f featureCountsFixture) ReadFeatureUse(_ context.Context, threads []string) (map[string]observation.FeatureUse, error) {
	values := make(map[string]observation.FeatureUse, len(threads))
	for _, thread := range threads {
		if value, ok := f[thread]; ok {
			values[thread] = value
		}
	}
	return values, nil
}

func TestUnavailableNativeFeaturesKeepSelectedDeduplication(t *testing.T) {
	for _, reader := range []FeatureReader{nil, featureCountsFixture{"root": {Spawns: 99}}} {
		use := ReadNativeFeatureUse(t.Context(), "root", []string{"child"}, reader)
		if use.Err == nil || len(use.Threads) != 1 || use.Threads[0] != "child" {
			t.Fatalf("failed native measurement lost selection: %+v", use)
		}
		capture := &transportcapture.MetricsSnapshot{OutputDeduplicationThreads: map[string]transportcapture.OutputDeduplicationMetrics{"root": {Occurrences: 2}, "child": {Occurrences: 3}, "foreign": {Occurrences: 99}}}
		metrics := NewFeatureMetrics("root", "Main", capture, true, use)
		if !metrics.Unavailable || metrics.Agents != 1 || metrics.Self.OutputDeduplication.Occurrences != 2 || metrics.Descendants.OutputDeduplication.Occurrences != 3 {
			t.Fatalf("failed native measurement changed independent deduplication: %+v", metrics)
		}
		for _, field := range featureFields(metrics, true) {
			if field.Group == "Spawns" && field.Label == "Total" && (len(field.Values) != 2 || field.Values[0] != "unavailable" || field.Values[1] != "unavailable") {
				t.Fatalf("failure became measured zero: %+v", field)
			}
		}
	}
}
