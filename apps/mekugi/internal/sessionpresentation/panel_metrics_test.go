package sessionpresentation

import (
	"context"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestFeatureMetricsSeparateSelectedThreadFromAllDescendants(t *testing.T) {
	counts := featureCountsFixture{
		"root":      {Spawns: 1, SpawnPreloads: 1},
		"worker":    {Spawns: 1, AllWaits: 1, OutputDeduplicationBytes: 80, OutputDeduplicationTokens: 20},
		"nested":    {CompletionChanges: 1, OutputDeduplicationBytes: 160, OutputDeduplicationTokens: 40},
		"unrelated": {SideQuestions: 1, OutputDeduplicationBytes: 999, OutputDeduplicationTokens: 249},
	}
	read := func(thread string) FeatureSnapshot {
		descendants := map[string][]string{"root": {"worker", "nested"}, "worker": {"nested"}}
		return ReadNativeFeatureUse(t.Context(), thread, descendants[thread], counts)
	}
	got := NewFeatureMetrics("worker", read("worker"))
	want := FeatureMetrics{
		Self:        FeatureCounts{FeatureUse: counts["worker"]},
		Descendants: FeatureCounts{FeatureUse: counts["nested"]},
		Thread:      "worker", Agents: 1,
	}
	if *got != want {
		t.Fatalf("worker features = %+v", *got)
	}
	counts["root"] = observation.FeatureUse{Spawns: 1, SpawnPreloads: 1, ProgressWaits: 1, ProgressYields: 1, AssignmentSources: 1, MessageSources: 1, SelfCompactionsCompleted: 1, SelfCompactionFailures: 1}
	counts["nested"] = observation.FeatureUse{CompletionChanges: 1, ProgressWaits: 2, ProgressYields: 2, AssignmentSources: 2, MessageSources: 2, SelfCompactionsCompleted: 2, SelfCompactionFailures: 2, OutputDeduplicationBytes: 160, OutputDeduplicationTokens: 40}
	root := NewFeatureMetrics("Main", read("root"))
	if root.Thread != "Main" || root.Agents != 2 || root.Self.SpawnPreloads != 1 || root.Descendants.AllWaits != 1 || root.Descendants.CompletionChanges != 1 || root.Descendants.OutputDeduplicationTokens != 60 || root.Descendants.OutputDeduplicationBytes != 240 || root.Descendants.SideQuestions != 0 {
		t.Fatalf("root features = %+v", *root)
	}
	cells := func(features *FeatureMetrics) map[[2]string]string {
		values := make(map[[2]string]string)
		for _, field := range MetricFields(&PanelView{Features: features}, MetricFeatures, 0) {
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

func TestNativeDeduplicationSavingsNeedCompleteFeatureRead(t *testing.T) {
	use := ReadNativeFeatureUse(t.Context(), "root", []string{"child"}, featureCountsFixture{
		"root": {OutputDeduplicationBytes: 512, OutputDeduplicationTokens: 128},
	})
	if use.Err == nil {
		t.Fatal("missing child measurement was accepted")
	}
	fields := MetricFields(&PanelView{Features: NewFeatureMetrics("Main", use)}, MetricFeatures, 0)
	found := 0
	for _, field := range fields {
		if field.Group == "Output deduplication" && !field.Heading && len(field.Values) > 0 {
			found++
			if strings.Join(field.Values, "/") != "unavailable/unavailable" {
				t.Fatalf("incomplete savings became a measured value: %+v", field)
			}
		}
	}
	if found != 2 {
		t.Fatalf("native savings rows missing: %+v", fields)
	}
}
