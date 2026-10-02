package router

import (
	"math"
	"sync"
	"testing"
)

func usageStoreFixture(t *testing.T, directory string) *mekugiReplayStore {
	t.Helper()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.close)
	return store
}

func storedUsageFixture(store *mekugiReplayStore) *threadUsage {
	u := newThreadUsage()
	u.store = store
	return u
}

func requireStoredUsage(t *testing.T, u *threadUsage, thread string, input, output, trips uint64, cost float64) tokenUsageReport {
	t.Helper()
	report, ok := u.snapshot(thread)
	gotCost := report.cost.cachedInput + report.cost.uncachedInput + report.cost.output
	if !ok || report.InputTokens != input || report.OutputTokens != output || u.roundtrips(thread) != trips || !report.cost.known || math.Abs(gotCost-cost) > 1e-10 {
		t.Fatalf("%s: report=%+v trips=%d cost=%g; want input=%d output=%d trips=%d cost=%g", thread, report, u.roundtrips(thread), gotCost, input, output, trips, cost)
	}
	return report
}

func TestThreadUsageStoreRestartFollowupAndIdentityIsolation(t *testing.T) {
	directory := t.TempDir()
	first := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(first.close)
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	observation := first.observation("child", "child", "grok:grok-4.6", "")
	observation.observe(counts)
	observation.observe(counts)
	observation.finish()
	first.observation("child", "child", "grok-4.6", "").observe(counts)
	first.observation("child", "child", "openai/gpt-6-sol", "").observe(counts)
	first.observation("root", "root", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	first.close()

	restored := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restored.close)
	restored.restore("child", true)
	report := requireStoredUsage(t, restored, "child", 300_000, 30_000, 3, .82)
	if !report.priorUnknown || report.missingUsage != 0 {
		t.Fatalf("restored counters claimed proven lifetime continuity or invented a request gap: %+v", report)
	}
	restored.restore("root", true)
	requireStoredUsage(t, restored, "root", 10, 0, 1, .00002)
	// A fork is another stable identity, not its parent's consumption record.
	restored.restore("fork", false)
	if _, ok := restored.snapshot("fork"); ok || restored.roundtrips("fork") != 0 {
		t.Fatal("fresh fork borrowed ancestor totals")
	}
	restored.observation("fork", "fork", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 1, UncachedInputTokens: 1})
	requireStoredUsage(t, restored, "fork", 1, 0, 1, .000002)
	restored.observation("child", "child", "gpt-6-sol", "").observe(counts)
	requireStoredUsage(t, restored, "child", 400_000, 40_000, 4, 1.12)
}

func TestThreadUsageStoreConcurrentOwnersMerge(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	owners := []*threadUsage{storedUsageFixture(store), storedUsageFixture(store)}
	for _, owner := range owners {
		t.Cleanup(owner.close)
		owner.restore("child", false)
	}
	var workers sync.WaitGroup
	for _, owner := range owners {
		workers.Go(func() {
			for range 20 {
				owner.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
			}
		})
	}
	workers.Wait()
	for _, owner := range owners {
		owner.close()
	}
	restored := storedUsageFixture(usageStoreFixture(t, directory))
	restored.restore("child", true)
	requireStoredUsage(t, restored, "child", 400, 40, 40, .0012)
}

func TestThreadUsageStoreRetainsHistoricalUnknownAndUsageGaps(t *testing.T) {
	directory := t.TempDir()
	first := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(first.close)
	first.restore("historical", true)
	first.observation("gap", "gap", "gpt-6-sol", "").finish()
	first.close()
	restored := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restored.close)
	restored.restore("historical", false)
	if report, ok := restored.snapshot("historical"); !ok || !report.priorUnknown || restored.roundtrips("historical") != 0 {
		t.Fatalf("historical unknown baseline lost: %+v valid=%v", report, ok)
	}
	restored.observation("historical", "historical", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	report := requireStoredUsage(t, restored, "historical", 100, 10, 1, .0003)
	if !report.priorUnknown {
		t.Fatal("followup falsely completed historical consumption")
	}
	restored.restore("gap", true)
	if report, ok := restored.snapshot("gap"); !ok || report.missingUsage != 1 || restored.roundtrips("gap") != 1 {
		t.Fatalf("retained request gap lost: %+v valid=%v", report, ok)
	}
	restored.observation("gap", "gap", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	report = requireStoredUsage(t, restored, "gap", 100, 10, 2, .0003)
	if report.missingUsage != 1 {
		t.Fatal("successful followup erased retained gap")
	}
	// Persist the lower bound and its unknown baseline through another restart.
	restored.close()
	again := storedUsageFixture(usageStoreFixture(t, directory))
	again.restore("historical", true)
	if report := requireStoredUsage(t, again, "historical", 100, 10, 1, .0003); !report.priorUnknown {
		t.Fatal("second restart lost historical gap")
	}
}
