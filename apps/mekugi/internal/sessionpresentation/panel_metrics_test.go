package sessionpresentation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestFeatureMetricsSeparateSelectedThreadFromAllDescendants(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	retained, err := history.NewService(control.RecordKinds())
	check(err)
	store, err := retained.Open(t.Context(), t.TempDir())
	check(err)
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	check(err)
	t.Cleanup(session.Close)
	service := control.New()
	check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "root-session", Task: "/root", AudienceAdmitted: true}))
	spawn := func(parent, turn, name, thread string) {
		t.Helper()
		caller := control.Identity{Thread: parent, Session: parent + "-session", Turn: turn, Call: "spawn-" + name, Item: "exec"}
		check(service.RequestStarted(t.Context(), caller, parent == "root"))
		reservation, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: name, Message: "work", Settings: control.Settings{Workspace: t.TempDir()}})
		check(err)
		for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
			_, err := service.ClaimEffect(t.Context(), parent, reservation.ID)
			check(err)
			check(service.EffectAcknowledged(t.Context(), parent, reservation.ID, control.Receipt{Phase: phase, Thread: thread, Session: thread + "-session", Turn: thread + "-turn"}))
		}
	}
	spawn("root", "root-turn", "worker", "worker")
	spawn("worker", "worker-turn", "nested", "nested")
	check(service.HostCompleted(t.Context(), "nested", "nested-turn", control.Completed, control.HostFailure{}))
	for thread, feature := range map[string]control.Feature{"root": control.FeatureSpawnPreload, "worker": control.FeatureWaitAll, "nested": control.FeatureCompletionChanges, "unrelated": control.FeatureSideQuestion} {
		service.ObserveFeature(thread, feature)
	}
	snapshot := &transportcapture.MetricsSnapshot{Schema: "mekugi.capture.metrics.v7", OutputDeduplicationThreads: map[string]transportcapture.OutputDeduplicationMetrics{
		"root": {Projections: 1, Occurrences: 1}, "worker": {Projections: 1, Occurrences: 2, SavedTokens: 20}, "nested": {Projections: 1, Occurrences: 4, SavedTokens: 40}, "unrelated": {Projections: 1, Occurrences: 8},
	}}
	got := NewFeatureMetrics("worker", service, snapshot, true)
	want := FeatureMetrics{
		Self:        FeatureCounts{FeatureUse: control.FeatureUse{Spawns: 1, AllWaits: 1}, OutputDeduplication: transportcapture.OutputDeduplicationMetrics{Projections: 1, Occurrences: 2, SavedTokens: 20}},
		Descendants: FeatureCounts{FeatureUse: control.FeatureUse{CompletionChanges: 1}, OutputDeduplication: transportcapture.OutputDeduplicationMetrics{Projections: 1, Occurrences: 4, SavedTokens: 40}},
		Thread:      "worker", Agents: 1,
	}
	if *got != want {
		t.Fatalf("worker features = %+v", *got)
	}
	newFeatures := []control.Feature{control.FeatureWaitProgress, control.FeatureProgressYield, control.FeatureAssignmentSources, control.FeatureMessageSources, control.FeatureSelfCompactionCompleted, control.FeatureCompactionCostSkip, control.FeatureCompactionPreflightSkip, control.FeatureSelfCompactionFailure}
	service.ObserveFeature("root", newFeatures...)
	for range 2 {
		service.ObserveFeature("nested", newFeatures...)
	}
	root := NewFeatureMetrics("root", service, snapshot, true)
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
		{"Compaction", "Self completed"}: "1/2", {"Compaction", "Cost skipped"}: "1/2", {"Compaction", "Preflight skipped"}: "1/2", {"Compaction", "Self failed"}: "1/2",
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
	off := cells(NewFeatureMetrics("root", service, snapshot, false))
	if _, counted := off[[2]string{"Output deduplication", "Occurrences"}]; counted || off[[2]string{"Output deduplication", ""}] != "Off for this launch." {
		t.Fatalf("disabled deduplication = %v", off)
	}
}
