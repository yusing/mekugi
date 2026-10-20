package router

import (
	"math"
	"testing"
)

func TestThreadUsageGapsPreserveObservedCountsModelsAndCosts(t *testing.T) {
	totals := newThreadUsage()
	first := tokenCounts{InputTokens: 100, UncachedInputTokens: 40, OutputTokens: 10, ReasoningTokens: 3}
	last := tokenCounts{InputTokens: 200, UncachedInputTokens: 50, OutputTokens: 20, ReasoningTokens: 5}
	totals.observation("root", "", "gpt-6-astra", "").observe(first)
	for range 7 {
		gap := totals.observation("root", "", "gpt-6-astra", "")
		gap.finish()
		gap.finish()
		// A repeated terminal on the same request must not erase the gap or count twice.
		gap.observe(first)
	}
	totals.observation("root", "", "gpt-5.6-sol", "").observe(last)
	got, ok := totals.snapshot("root")
	if !ok || got.Incomplete || got.missingUsage != 7 ||
		got.InputTokens != 300 || got.UncachedInputTokens != 90 ||
		got.OutputTokens != 30 || got.ReasoningTokens != 8 ||
		got.model != "gpt-6-astra, gpt-5.6-sol" {
		t.Fatalf("lost observed usage: %+v, ok=%t", got, ok)
	}
	cost := estimateTokenCost("gpt-6-astra", "", first)
	cost.add(estimateTokenCost("gpt-5.6-sol", "", last))
	if !got.cost.known || math.Abs(got.cost.cachedInput-cost.cachedInput) > 1e-12 ||
		math.Abs(got.cost.uncachedInput-cost.uncachedInput) > 1e-12 ||
		math.Abs(got.cost.output-cost.output) > 1e-12 {
		t.Fatalf("cost repriced or included missing responses: %+v want %+v", got.cost, cost)
	}
}

func TestThreadUsageGapOnlyRetainsModelAndUnknownPricingRemainsUnknown(t *testing.T) {
	totals := newThreadUsage()
	totals.observation("root", "", "gpt-6-astra", "").finish()
	got, ok := totals.snapshot("root")
	if !ok || got.missingUsage != 1 || got.InputTokens != 0 || got.model != "gpt-6-astra" {
		t.Fatalf("gap-only observation lost: %+v, ok=%t", got, ok)
	}
	totals.observation("root", "", "unknown-model", "").observe(tokenCounts{InputTokens: 10})
	got, ok = totals.snapshot("root")
	if !ok || got.InputTokens != 10 || got.missingUsage != 1 || got.cost.known {
		t.Fatalf("missing usage hid counts or invented pricing: %+v, ok=%t", got, ok)
	}
}
