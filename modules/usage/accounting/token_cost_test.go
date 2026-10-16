package accounting

import (
	"math"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestTokenCostDisjointCategories(t *testing.T) {
	counts := inference.Usage{InputTokens: 100_000, UncachedInputTokens: 40_000, OutputTokens: 30_000, ReasoningTokens: 20_000}
	for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra"} {
		cost := estimateReferenceCost(model, "", counts)
		if !cost.Known || cost.UncachedInput != 0.4 || cost.CachedInput != 0.06 || cost.Output != 1.5 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	for _, model := range []string{"", "unknown", "grok:unknown", "other/gpt-6-astra", "gpt-6-astra-unknown", "x-ai/grok-4.6"} {
		if estimateReferenceCost(model, "", counts).Known {
			t.Fatalf("invented price for %q", model)
		}
	}
	for _, model := range []string{"grok:grok-4.6", "grok-4.6", "GROK:GROK-4.6"} {
		cost := estimateReferenceCost(model, "", counts)
		if !cost.Known || cost.UncachedInput != 0.08 || cost.CachedInput != 0.03 || cost.Output != 0.18 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	for model, want := range map[string]float64{"gpt-6-sol": 0.392, "gpt-6.1-sol": 0.386, "openai/gpt-6.1-sol": 0.386, "gpt-6-luna": 0.0196} {
		cost := estimateReferenceCost(model, "", counts)
		if !cost.Known || math.Abs(cost.UncachedInput+cost.CachedInput+cost.Output-want) > 1e-10 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	if estimateReferenceCost("gpt-6-astra", "", inference.Usage{InputTokens: 1, UncachedInputTokens: 2}).Known {
		t.Fatal("priced inconsistent input")
	}
	if cost := estimateReferenceCost("gpt-6-astra", "", inference.Usage{}); !cost.Known || cost.Output != 0 {
		t.Fatal("zero usage must have a known zero estimate", cost)
	}
}

func TestTokenCostContextTierBoundary(t *testing.T) {
	for _, tc := range []struct {
		model                             string
		input                             uint64
		wantInput, wantCached, wantOutput float64
	}{
		{"gpt-6-astra", 271_999, 1, 0.171999, 0.5},
		{"gpt-6-astra", 272_000, 1, 0.172, 0.5},
		{"gpt-6-astra", 272_001, 2, 0.344002, 0.75},
		{"gpt-6.1-sol", 271_999, 0.2, 0.0171999, 0.1},
		{"gpt-6.1-sol", 272_000, 0.2, 0.0172, 0.1},
		{"gpt-6.1-sol", 272_001, 0.4, 0.0344002, 0.15},
		{"gpt-5.4-mini", 272_000, 0.075, 0.0129, 0.045},
	} {
		counts := inference.Usage{InputTokens: tc.input, UncachedInputTokens: 100_000, OutputTokens: 10_000}
		cost := estimateReferenceCost(tc.model, "", counts)
		if !cost.Known || math.Abs(cost.UncachedInput-tc.wantInput) > 1e-10 ||
			math.Abs(cost.CachedInput-tc.wantCached) > 1e-10 || math.Abs(cost.Output-tc.wantOutput) > 1e-10 {
			t.Fatalf("%s input=%d: %+v", tc.model, tc.input, cost)
		}
	}
}

func TestTokenCostGrokContextTierAndUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		model, tier                       string
		input                             uint64
		writes                            uint64
		wantInput, wantCached, wantOutput float64
		known                             bool
	}{
		{"grok:grok-4.6", "", 199_999, 0, 0.2, 0.0499995, 0.06, true},
		{"grok:grok-4.5", "", 199_999, 0, 0.2, 0.0299997, 0.06, true},
		{"grok:grok-4.7", "", 199_999, 0, 0.2, 0.0499995, 0.06, true},
		{"grok:grok-4.7-build-fast", "", 199_999, 0, 0.4, 0.099999, 0.12, true},
		{"grok:grok-4.7-build-fast", "", 200_000, 0, 0.4, 0.1, 0.12, true},
		{"grok:grok-4.7-build-fast", "", 200_001, 0, 0.6, 0.1500015, 0.18, true},
		{"grok-4.6", "default", 200_000, 0, 0.4, 0.1, 0.12, true},
		{"grok:grok-4.6", "", 200_001, 0, 0.4, 0.100001, 0.12, true},
		{"grok:grok-4.6", "fast", 100_000, 0, 0, 0, 0, false},
		{"grok:grok-4.6", "priority", 200_000, 0, 0, 0, 0, false},
		{"grok:grok-4.6", "", 100_000, 1, 0, 0, 0, false},
	} {
		counts := inference.Usage{InputTokens: tc.input, UncachedInputTokens: 100_000, CacheWriteTokens: tc.writes, OutputTokens: 10_000, ReasoningTokens: 5_000}
		got := estimateReferenceCost(tc.model, tc.tier, counts)
		if got.Known != tc.known || math.Abs(got.UncachedInput-tc.wantInput) > 1e-10 || math.Abs(got.CachedInput-tc.wantCached) > 1e-10 || math.Abs(got.Output-tc.wantOutput) > 1e-10 {
			t.Fatalf("%s/%s/%d: %+v", tc.model, tc.tier, tc.input, got)
		}
	}
}

func TestThreadCostsKeepPerResponseModelsAndTiers(t *testing.T) {
	totals := newTestUsage(t)
	counts := inference.Usage{InputTokens: 200_000, UncachedInputTokens: 100_000, OutputTokens: 10_000}
	first := totals.Observation("root", "", "gpt-6-astra", "")
	first.Observe(counts)
	first.Observe(counts)
	totals.Observation("child", "", "gpt-6-astra", "").Observe(counts)
	totals.Observation("root", "", "gpt-5.6-sol", "").Observe(counts)
	report, ok := totals.Snapshot("root")
	if !ok || report.InputTokens != 400_000 || !report.Cost.Known {
		t.Fatal("lost cumulative usage or duplicate observation counted", report, ok)
	}
	if got := report.Cost.UncachedInput + report.Cost.CachedInput + report.Cost.Output; math.Abs(got-2.24) > 1e-10 {
		t.Fatal("repriced history, combined context tiers, or included child usage", got)
	}
	// Once a response is unpriced, later priced usage cannot restore completeness.
	totals.Observation("root", "", "unknown", "").Observe(counts)
	totals.Observation("root", "", "gpt-6-astra", "").Observe(counts)
	report, ok = totals.Snapshot("root")
	if !ok || report.InputTokens != 800_000 || report.Cost.Known {
		t.Fatal("partial cost presented as complete or token counts lost", report, ok)
	}
}
