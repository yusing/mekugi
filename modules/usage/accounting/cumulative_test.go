package accounting

import (
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/persistence"
)

func TestObserveCumulativeRepeatedSnapshotsAndMultipleRounds(t *testing.T) {
	u := newTestUsage(t)
	first := Report{
		InputTokens: 100, UncachedInputTokens: 60, CacheWriteTokens: 20, OutputTokens: 30, ReasoningTokens: 10,
		Cost: Cost{Reported: 6, Known: true}, Model: "claude-native", Roundtrips: 2,
	}
	for range 50 {
		u.ObserveCumulative("child", first)
	}
	if got, ok := u.Snapshot("child"); !ok || got != first {
		t.Fatalf("repeated snapshot changed canonical totals: %+v, valid=%v; want %+v", got, ok, first)
	}
	next := first
	next.InputTokens, next.UncachedInputTokens, next.CacheWriteTokens = 250, 140, 35
	next.OutputTokens, next.ReasoningTokens, next.Roundtrips = 75, 25, 5
	next.Cost = Cost{Reported: 15, Known: true}
	next.Model = "claude-native, claude-next"
	// A cumulative report cannot supply a request-paired throughput measurement.
	next.RoundOutput.StartedUnixNano = 1
	u.ObserveCumulative("child", next)
	u.ObserveCumulative("child", next)
	next.RoundOutput = RoundOutput{}
	if got, ok := u.Snapshot("child"); !ok || got != next {
		t.Fatalf("growth was repriced, doubled, or assigned timing: %+v, valid=%v; want %+v", got, ok, next)
	}
	if got, ok := u.Snapshot("fork"); ok || got.Roundtrips != 0 {
		t.Fatalf("another identity inherited native consumption: %+v, valid=%v", got, ok)
	}
}

func TestObserveCumulativeRetainsDeltasAndRestoresCanonicalBaseline(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	store.writes = new(persistence.Counter)
	u := storedUsageFixture(t, store)
	u.MarkNew("child")
	unlock := holdUsageStoreLock(t, store)
	report := Report{InputTokens: 100, UncachedInputTokens: 70, CacheWriteTokens: 20, OutputTokens: 30, ReasoningTokens: 10,
		Cost: Cost{Reported: 6, Known: true}, Model: "claude-native", Roundtrips: 3}
	requireUsageOperationPrompt(t, func() {
		for range 100 {
			u.ObserveCumulative("child", report)
		}
	})
	if got, ok := u.Snapshot("child"); !ok || got != report {
		t.Fatalf("pending cumulative totals unavailable: %+v, valid=%v", got, ok)
	}
	report.Model = "claude-native, claude-next"
	u.ObserveCumulative("child", report)
	unlock()
	flushUsageForTest(t, u)
	written := store.writes.Snapshot().Bytes
	for range 100 {
		u.ObserveCumulative("child", report)
	}
	flushUsageForTest(t, u)
	if got := store.writes.Snapshot().Bytes; got != written {
		t.Fatalf("duplicate snapshots published more bytes: %d -> %d", written, got)
	}
	u.Close()

	restored := storedUsageFixture(t, usageStoreFixture(t, directory))
	restored.ObserveCumulative("child", report)
	report.PriorUnknown = true // Retention cannot prove uninterrupted coverage.
	if got, ok := restored.Snapshot("child"); !ok || got != report {
		t.Fatalf("repeated snapshot doubled restored totals: %+v, valid=%v; want %+v", got, ok, report)
	}
	report.InputTokens, report.UncachedInputTokens, report.CacheWriteTokens = 200, 140, 40
	report.OutputTokens, report.ReasoningTokens, report.Roundtrips = 60, 20, 6
	report.Cost = Cost{Reported: 12, Known: true}
	restored.ObserveCumulative("child", report)
	restored.Close()
	again := storedUsageFixture(t, usageStoreFixture(t, directory))
	if got, ok := again.Snapshot("child"); !ok || got != report {
		t.Fatalf("followup did not retain only its cumulative delta: %+v, valid=%v; want %+v", got, ok, report)
	}
}

func TestObserveCumulativeRegressionPreservesKnownTotals(t *testing.T) {
	for name, regress := range map[string]func(*Report){
		"input":          func(r *Report) { r.InputTokens-- },
		"uncached input": func(r *Report) { r.UncachedInputTokens-- },
		"cache write":    func(r *Report) { r.CacheWriteTokens-- },
		"output":         func(r *Report) { r.OutputTokens-- },
		"reasoning":      func(r *Report) { r.ReasoningTokens-- },
		"missing usage":  func(r *Report) { r.MissingUsage-- },
		"roundtrips":     func(r *Report) { r.Roundtrips-- },
		"uncached cost":  func(r *Report) { r.Cost.UncachedInput-- },
		"cached cost":    func(r *Report) { r.Cost.CachedInput-- },
		"output cost":    func(r *Report) { r.Cost.Output-- },
		"reported cost":  func(r *Report) { r.Cost.Reported-- },
	} {
		t.Run(name, func(t *testing.T) {
			u := newTestUsage(t)
			first := Report{InputTokens: 100, UncachedInputTokens: 60, CacheWriteTokens: 20, OutputTokens: 30, ReasoningTokens: 10,
				Cost: Cost{UncachedInput: 3, CachedInput: 1, Output: 2, Reported: 4, Known: true}, Model: "claude-native", Roundtrips: 3, MissingUsage: 1}
			u.ObserveCumulative("child", first)
			regressed := first
			regress(&regressed)
			u.ObserveCumulative("child", regressed)
			first.PriorUnknown = true
			if got, ok := u.Snapshot("child"); !ok || got != first {
				t.Fatalf("regression subtracted totals or hid uncertainty: %+v, valid=%v; want %+v", got, ok, first)
			}
			first.InputTokens, first.OutputTokens, first.Roundtrips = 120, 40, 4
			u.ObserveCumulative("child", first)
			if got, ok := u.Snapshot("child"); !ok || got != first {
				t.Fatalf("growth after regression reset the baseline: %+v, valid=%v; want %+v", got, ok, first)
			}
		})
	}
}

func TestObserveCumulativePreservesPartialEvidenceAndKnownCostAmounts(t *testing.T) {
	u := newTestUsage(t)
	u.Observation("child", "child", "claude-native", "").Finish()
	report := Report{InputTokens: 100, UncachedInputTokens: 60, OutputTokens: 30,
		Cost: Cost{Reported: 6, Known: true}, Model: "claude-native", Roundtrips: 3, PriorUnknown: true, Inconsistent: true}
	u.ObserveCumulative("child", report)
	report.Cost = Cost{}
	report.PriorUnknown, report.Inconsistent = false, false
	u.ObserveCumulative("child", report)
	report.Cost = Cost{Reported: 6, Known: true}
	u.ObserveCumulative("child", report)
	want := report
	want.Cost.Known, want.PriorUnknown, want.Inconsistent, want.MissingUsage = false, true, true, 1
	if got, ok := u.Snapshot("child"); !ok || got != want {
		t.Fatalf("later snapshots erased partial evidence or known cost amounts: %+v, valid=%v; want %+v", got, ok, want)
	}
	u.Observation("conflicted", "other", "claude-native", "").Observe(inference.Usage{InputTokens: 1})
	u.ObserveCumulative("conflicted", report)
	if got, ok := u.Snapshot("conflicted"); ok || got.Roundtrips != report.Roundtrips {
		t.Fatalf("cumulative snapshot revived an invalid total: %+v, valid=%v", got, ok)
	}
}

func TestCostTotalIncludesReportedEstimates(t *testing.T) {
	cost := Cost{UncachedInput: 1, CachedInput: 2, Output: 3, Reported: 4, Known: true}
	cost.add(Cost{Reported: 5, Known: true})
	if !cost.Known || cost.Reported != 9 || cost.Total() != 15 {
		t.Fatalf("native reported estimate was omitted or assigned to a category: %+v, total=%g", cost, cost.Total())
	}
}
