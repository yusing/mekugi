package accounting

import (
	"fmt"
	"math"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestTokenCostServiceTiers(t *testing.T) {
	for _, tc := range []struct {
		model, tier              string
		input                    uint64
		uncached, cached, output float64
		known                    bool
	}{
		{"gpt-6-astra", "default", 200_000, 1, .1, .5, true},
		{"gpt-6-astra", "priority", 200_000, 2, .2, 1, true},
		{"gpt-6-sol", "default", 200_000, .2, .02, .1, true},
		{"gpt-6-sol", "fast", 200_000, .4, .04, .2, true},
		{"gpt-6.1-sol", "default", 200_000, .2, .01, .1, true},
		{"gpt-6.1-sol", "fast", 200_000, .4, .02, .2, true},
		{"openai/gpt-6.1-sol", "priority", 200_000, .4, .02, .2, true},
		{"gpt-6.1-sol", "fast", 272_000, .4, .0344, .2, true},
		{"gpt-6.1-sol", "priority", 272_001, .8, .0688004, .3, true},
		{"gpt-6-luna", "default", 200_000, .01, .001, .005, true},
		{"gpt-6-luna", "fast", 200_000, .02, .002, .01, true},
		{"openai/gpt-6-astra", "fast", 200_000, 2, .2, 1, true},
		{"gpt-6-astra", "fast", 272_000, 2, .344, 1, true},
		{"gpt-6-astra", "fast", 272_001, 4, .688004, 1.5, true},
		{"gpt-5.6-sol", "priority", 200_000, .8, .08, .4, true},
		{"gpt-5.6-sol", "fast", 272_000, .8, .1376, .4, true},
		{"gpt-5.6-sol", "fast", 272_001, 1.6, .2752016, .6, true},
		{"gpt-5.6-terra", "fast", 200_000, .4, .04, .24, true},
		{"gpt-5.6-terra", "priority", 272_001, .8, .1376008, .36, true},
		{"gpt-5.6-luna", "priority", 200_000, .04, .004, .024, true},
		{"gpt-5.6-luna", "fast", 272_001, .08, .01376008, .036, true},
		{"gpt-5.5", "fast", 271_999, 1.25, .21499875, .75, true},
		{"gpt-5.5", "priority", 272_000, 1.25, .215, .75, true},
		{"gpt-5.5", "priority", 272_001, 0, 0, 0, false},
		{"gpt-5.4", "priority", 271_999, .5, .0859995, .3, true},
		{"gpt-5.4", "fast", 272_000, .5, .086, .3, true},
		{"gpt-5.4", "fast", 272_001, 0, 0, 0, false},
		{"gpt-5.4-mini", "fast", 272_000, .15, .0258, .09, true},
		{"gpt-5.4-nano", "priority", 200_000, 0, 0, 0, false},
		{"gpt-6-astra-pro", "fast", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "auto", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "flex", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "unknown", 200_000, 0, 0, 0, false},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.model, tc.tier, tc.input), func(t *testing.T) {
			got := estimateReferenceCost(tc.model, tc.tier, inference.Usage{InputTokens: tc.input, UncachedInputTokens: 100_000, OutputTokens: 10_000, ReasoningTokens: 5_000})
			if got.Known != tc.known || math.Abs(got.UncachedInput-tc.uncached) > 1e-10 || math.Abs(got.CachedInput-tc.cached) > 1e-10 || math.Abs(got.Output-tc.output) > 1e-10 {
				t.Fatalf("cost=%+v, want %+v", got, tc)
			}
		})
	}
}

func TestThreadCostsKeepResponseServiceTiers(t *testing.T) {
	totals := newTestUsage(t)
	counts := inference.Usage{InputTokens: 100_000, UncachedInputTokens: 60_000, OutputTokens: 10_000, ReasoningTokens: 5_000}
	totals.Observation("thread", "", "gpt-6-astra", "priority").Observe(counts)
	counts.ServiceTier = "default"
	totals.Observation("thread", "", "gpt-5.6-sol", "fast").Observe(counts)
	counts.ServiceTier = "priority"
	totals.Observation("thread", "", "gpt-5.6-luna", "default").Observe(counts)
	got, ok := totals.Snapshot("thread")
	// Astra Fast 2.28 + Sol default .456 + Luna Fast .0496.
	if !ok || !got.Cost.Known || got.InputTokens != 300_000 || math.Abs(got.Cost.UncachedInput+got.Cost.CachedInput+got.Cost.Output-2.7856) > 1e-10 {
		t.Fatalf("report=%+v valid=%t", got, ok)
	}
	counts.ServiceTier = "unpriced"
	totals.Observation("thread", "", "gpt-5.6-sol", "default").Observe(counts)
	counts.ServiceTier = "default"
	totals.Observation("thread", "", "gpt-5.6-sol", "default").Observe(counts)
	got, ok = totals.Snapshot("thread")
	if !ok || got.Cost.Known || got.InputTokens != 500_000 {
		t.Fatalf("unpriced tier lost counts or revived partial costs: %+v", got)
	}
}

func TestTokenCostCacheWrites(t *testing.T) {
	for _, tc := range []struct {
		model, tier             string
		input                   uint64
		wantUncached, wantTotal float64
	}{
		{"gpt-6-astra", "default", 200_000, 1.1, 1.7},
		{"gpt-6-astra", "fast", 200_000, 2.2, 3.4},
		{"gpt-6-astra", "priority", 300_000, 4.4, 6.7},
		{"gpt-6.1-sol", "default", 200_000, .22, .33},
		{"gpt-6.1-sol", "default", 300_000, .44, .63},
		{"gpt-6.1-sol", "fast", 200_000, .44, .66},
		{"gpt-6.1-sol", "priority", 300_000, .88, 1.26},
		{"gpt-5.6-sol", "default", 200_000, .44, .68},
		{"gpt-5.6-sol", "priority", 200_000, .88, 1.36},
		{"gpt-5.6-sol", "fast", 300_000, 1.76, 2.68},
		{"gpt-5.6-terra", "priority", 200_000, .44, .72},
		{"gpt-5.6-luna", "fast", 200_000, .044, .072},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.model, tc.tier, tc.input), func(t *testing.T) {
			counts := inference.Usage{InputTokens: tc.input, UncachedInputTokens: 100_000, CacheWriteTokens: 40_000, OutputTokens: 10_000, ReasoningTokens: 5_000}
			cost := estimateReferenceCost(tc.model, tc.tier, counts)
			if !cost.Known || math.Abs(cost.UncachedInput-tc.wantUncached) > 1e-10 || math.Abs(cost.UncachedInput+cost.CachedInput+cost.Output-tc.wantTotal) > 1e-10 {
				t.Fatalf("cost=%+v want=%+v", cost, tc)
			}
			totals := newTestUsage(t)
			for range 2 {
				totals.Observation("thread", "", tc.model, tc.tier).Observe(counts)
			}
			report, ok := totals.Snapshot("thread")
			if !ok || report.InputTokens != 2*tc.input || report.CacheWriteTokens != 80_000 {
				t.Fatalf("report=%+v", report)
			}
		})
	}
	for _, counts := range []inference.Usage{
		{InputTokens: 10, UncachedInputTokens: 5, CacheWriteTokens: 6},
		{InputTokens: 10, UncachedInputTokens: 5, CacheWriteTokens: 4, Incomplete: true},
	} {
		if estimateReferenceCost("gpt-6-astra", "priority", counts).Known {
			t.Fatal("priced invalid cache writes")
		}
	}
	if estimateReferenceCost("gpt-5.5", "priority", inference.Usage{InputTokens: 10, UncachedInputTokens: 10, CacheWriteTokens: 5}).Known {
		t.Fatal("invented unavailable model cache-write price")
	}
}
