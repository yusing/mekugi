package accounting

import (
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestThreadUsageConflictDoesNotResumeWithPartialTotals(t *testing.T) {
	totals := newTestUsage(t)
	totals.Observation("transport", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 10})
	totals.Observation("other", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 20})
	totals.Observation("transport", "other", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 30})
	totals.Observation("transport", "transport", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 40})
	if _, valid := totals.Snapshot("transport"); valid {
		t.Fatal("conflicting identity later reported a partial lifetime total")
	}
	if got, valid := totals.Snapshot("other"); !valid || got.InputTokens != 20 {
		t.Fatal("conflicting metadata changed another transport thread", got, valid)
	}
}
