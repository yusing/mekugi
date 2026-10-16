package accounting

import (
	"math"
	"sync"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func requireStoredUsage(t *testing.T, u *Service, thread string, input, output, trips uint64, cost float64) Report {
	t.Helper()
	report, ok := u.Snapshot(thread)
	gotCost := report.Cost.CachedInput + report.Cost.UncachedInput + report.Cost.Output
	if !ok || report.InputTokens != input || report.OutputTokens != output || report.Roundtrips != trips || !report.Cost.Known || math.Abs(gotCost-cost) > 1e-10 {
		t.Fatalf("%s: report=%+v trips=%d cost=%g; want input=%d output=%d trips=%d cost=%g", thread, report, report.Roundtrips, gotCost, input, output, trips, cost)
	}
	return report
}

func TestThreadUsageStoreRestartFollowupAndIdentityIsolation(t *testing.T) {
	directory := t.TempDir()
	first := storedUsageFixture(t, usageStoreFixture(t, directory))

	counts := inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	observation := first.Observation("child", "child", "grok:grok-4.6", "")
	observation.Observe(counts)
	observation.Observe(counts)
	observation.Finish()
	first.Observation("child", "child", "grok-4.6", "").Observe(counts)
	first.Observation("child", "child", "openai/gpt-6-sol", "").Observe(counts)
	first.Observation("root", "root", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10})
	first.Close()

	restored := storedUsageFixture(t, usageStoreFixture(t, directory))

	restored.Restore("child", true)
	report := requireStoredUsage(t, restored, "child", 300_000, 30_000, 3, .82)
	if !report.PriorUnknown || report.MissingUsage != 0 {
		t.Fatalf("restored counters claimed proven lifetime continuity or invented a request gap: %+v", report)
	}
	restored.Restore("root", true)
	requireStoredUsage(t, restored, "root", 10, 0, 1, .00002)
	// A fork is another stable identity, not its parent's consumption record.
	restored.Restore("fork", false)
	if report, ok := restored.Snapshot("fork"); ok || report.Roundtrips != 0 {
		t.Fatal("fresh fork borrowed ancestor totals")
	}
	restored.Observation("fork", "fork", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 1, UncachedInputTokens: 1})
	requireStoredUsage(t, restored, "fork", 1, 0, 1, .000002)
	restored.Observation("child", "child", "gpt-6-sol", "").Observe(counts)
	requireStoredUsage(t, restored, "child", 400_000, 40_000, 4, 1.12)
}

func TestThreadUsageStoreConcurrentOwnersMerge(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	owners := []*Service{storedUsageFixture(t, store), storedUsageFixture(t, store)}
	for _, owner := range owners {

		owner.Restore("child", false)
	}
	var workers sync.WaitGroup
	for _, owner := range owners {
		workers.Go(func() {
			for range 20 {
				owner.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
			}
		})
	}
	workers.Wait()
	for _, owner := range owners {
		owner.Close()
	}
	restored := storedUsageFixture(t, usageStoreFixture(t, directory))
	restored.Restore("child", true)
	requireStoredUsage(t, restored, "child", 400, 40, 40, .0012)
}

func TestThreadUsageStoreRetainsHistoricalUnknownAndUsageGaps(t *testing.T) {
	directory := t.TempDir()
	first := storedUsageFixture(t, usageStoreFixture(t, directory))

	first.Restore("historical", true)
	first.Observation("gap", "gap", "gpt-6-sol", "").Finish()
	first.Close()
	restored := storedUsageFixture(t, usageStoreFixture(t, directory))

	restored.Restore("historical", false)
	if report, ok := restored.Snapshot("historical"); !ok || !report.PriorUnknown || report.Roundtrips != 0 {
		t.Fatalf("historical unknown baseline lost: %+v valid=%v", report, ok)
	}
	restored.Observation("historical", "historical", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	report := requireStoredUsage(t, restored, "historical", 100, 10, 1, .0003)
	if !report.PriorUnknown {
		t.Fatal("followup falsely completed historical consumption")
	}
	restored.Restore("gap", true)
	if report, ok := restored.Snapshot("gap"); !ok || report.MissingUsage != 1 || report.Roundtrips != 1 {
		t.Fatalf("retained request gap lost: %+v valid=%v", report, ok)
	}
	restored.Observation("gap", "gap", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	report = requireStoredUsage(t, restored, "gap", 100, 10, 2, .0003)
	if report.MissingUsage != 1 {
		t.Fatal("successful followup erased retained gap")
	}
	// Persist the lower bound and its unknown baseline through another restart.
	restored.Close()
	again := storedUsageFixture(t, usageStoreFixture(t, directory))
	again.Restore("historical", true)
	if report := requireStoredUsage(t, again, "historical", 100, 10, 1, .0003); !report.PriorUnknown {
		t.Fatal("second restart lost historical gap")
	}
}
