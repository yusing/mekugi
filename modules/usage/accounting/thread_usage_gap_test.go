package accounting

import (
	"math"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestThreadUsageGapsPreserveObservedCountsModelsAndCosts(t *testing.T) {
	totals := newTestUsage(t)
	first := inference.Usage{InputTokens: 100, UncachedInputTokens: 40, OutputTokens: 10, ReasoningTokens: 3}
	last := inference.Usage{InputTokens: 200, UncachedInputTokens: 50, OutputTokens: 20, ReasoningTokens: 5}
	totals.Observation("root", "", "gpt-6-astra", "").Observe(first)
	for range 7 {
		gap := totals.Observation("root", "", "gpt-6-astra", "")
		gap.Finish()
		gap.Finish()
		// A repeated terminal on the same request must not erase the gap or count twice.
		gap.Observe(first)
	}
	totals.Observation("root", "", "gpt-5.6-sol", "").Observe(last)
	got, ok := totals.Snapshot("root")
	if !ok || got.Incomplete || got.MissingUsage != 7 ||
		got.InputTokens != 300 || got.UncachedInputTokens != 90 ||
		got.OutputTokens != 30 || got.ReasoningTokens != 8 ||
		got.Model != "gpt-6-astra, gpt-5.6-sol" {
		t.Fatalf("lost observed usage: %+v, ok=%t", got, ok)
	}
	cost := estimateReferenceCost("gpt-6-astra", "", first)
	cost.add(estimateReferenceCost("gpt-5.6-sol", "", last))
	if !got.Cost.Known || math.Abs(got.Cost.CachedInput-cost.CachedInput) > 1e-12 ||
		math.Abs(got.Cost.UncachedInput-cost.UncachedInput) > 1e-12 ||
		math.Abs(got.Cost.Output-cost.Output) > 1e-12 {
		t.Fatalf("cost repriced or included missing responses: %+v want %+v", got.Cost, cost)
	}
}

func TestThreadUsageGapOnlyRetainsModelAndUnknownPricingRemainsUnknown(t *testing.T) {
	totals := newTestUsage(t)
	totals.Observation("root", "", "gpt-6-astra", "").Finish()
	got, ok := totals.Snapshot("root")
	if !ok || got.MissingUsage != 1 || got.InputTokens != 0 || got.Model != "gpt-6-astra" {
		t.Fatalf("gap-only observation lost: %+v, ok=%t", got, ok)
	}
	totals.Observation("root", "", "unknown-model", "").Observe(inference.Usage{InputTokens: 10})
	got, ok = totals.Snapshot("root")
	if !ok || got.InputTokens != 10 || got.MissingUsage != 1 || got.Cost.Known {
		t.Fatalf("missing usage hid counts or invented pricing: %+v, ok=%t", got, ok)
	}
}
