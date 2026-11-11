package accounting

import (
	"math"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestCompareInputCostUsesLatestActualInput(t *testing.T) {
	u := New()
	u.Observation("root", "root", "gpt-5.4", "").Observe(inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000})
	u.Observation("root", "root", "gpt-5.4", "").Observe(inference.Usage{InputTokens: 1000, UncachedInputTokens: 100, OutputTokens: 9000})
	current, cold, known := u.CompareInputCost("root", "gpt-5.4", "default", 500)
	if !known || math.Abs(current.Total()-.000475) > 1e-12 || math.Abs(cold.Total()-.00125) > 1e-12 {
		t.Fatalf("latest input pricing: current=%+v cold=%+v known=%v", current, cold, known)
	}
	for _, query := range [][3]string{{"foreign", "gpt-5.4", ""}, {"root", "gpt-5.5", ""}, {"root", "gpt-5.4", "priority"}} {
		if _, _, known := u.CompareInputCost(query[0], query[1], query[2], 500); known {
			t.Fatalf("borrowed another input's pricing: %v", query)
		}
	}
	u.Observation("root", "root", "gpt-5.4", "").Finish()
	if _, _, known := u.CompareInputCost("root", "gpt-5.4", "", 500); known {
		t.Fatal("missing terminal usage revived stale pricing")
	}
}

func TestCompactionCostCacheDiscountComparisons(t *testing.T) {
	// Historical token counts and reset estimates. Output tokens are irrelevant:
	// the gate compares one next input, not total or amortized task cost.
	for _, test := range []struct {
		input, cached, reset uint64
		current, cold        float64
	}{
		{179490, 179200, 29828, .182100, .298280},
		{184541, 184320, 21923, .186530, .219230},
		{222741, 222464, 25212, .225234, .252120},
		{181341, 181120, 26989, .183330, .269890},
	} {
		u := New()
		u.Observation("root", "root", "gpt-6-astra", "default").Observe(inference.Usage{InputTokens: test.input, UncachedInputTokens: test.input - test.cached})
		current, cold, known := u.CompareInputCost("root", "gpt-6-astra", "default", test.reset)
		if !known || math.Abs(current.Total()-test.current) > 1e-12 || math.Abs(cold.Total()-test.cold) > 1e-12 || cold.Total() <= current.Total() {
			t.Fatalf("cache-discount comparison: current=%+v cold=%+v known=%t", current, cold, known)
		}
	}
}

func TestCompareInputCostPinsRatesAndRequestOrdering(t *testing.T) {
	u := New()
	observe := func(start int64, input uint64) {
		o := u.Observation("root", "root", "catalog-model", "")
		o.started = time.Unix(0, start)
		o.Price = func(tier string, counts inference.Usage) Cost {
			rate := 2.0
			if counts.InputTokens > 1000 {
				rate = 4
			}
			return Cost{
				UncachedInput: float64(counts.UncachedInputTokens) * rate / 1e6,
				CachedInput:   float64(counts.InputTokens-counts.UncachedInputTokens) * .5 / 1e6,
				Output:        float64(counts.OutputTokens) / 1e6,
				Known:         true,
			}
		}
		o.Observe(inference.Usage{InputTokens: input, UncachedInputTokens: input / 2})
	}
	observe(2, 1000)
	observe(1, 500)
	current, cold, known := u.CompareInputCost("root", "catalog-model", "", 1001)
	if !known || math.Abs(current.Total()-.00125) > 1e-12 || math.Abs(cold.Total()-.004004) > 1e-12 {
		t.Fatalf("pinned rates or ordering lost: %+v %+v %v", current, cold, known)
	}
}

func TestInputCostSurvivesLivePublicationButNotRestore(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)
	u.Observation("root", "root", "gpt-5.4", "").Observe(inference.Usage{InputTokens: 1000, UncachedInputTokens: 100})
	flushUsageForTest(t, u)
	if _, _, known := u.CompareInputCost("root", "gpt-5.4", "", 500); !known {
		t.Fatal("publication discarded live pricing")
	}
	restored := storedUsageFixture(t, store)
	restored.Restore("root", true)
	if _, _, known := restored.CompareInputCost("root", "gpt-5.4", "", 500); known {
		t.Fatal("restored totals became a current request")
	}
	next := u.Observation("root", "root", "gpt-5.4", "")
	next.Begin()
	if _, _, known := u.CompareInputCost("root", "gpt-5.4", "", 500); known {
		t.Fatal("in-flight request borrowed stale cache evidence")
	}
	next.Finish()
}
