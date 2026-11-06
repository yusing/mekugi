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
			return EstimateRates(Rates{Input: &rate, Output: new(1.0), CacheRead: new(.5)}, tier, counts)
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
