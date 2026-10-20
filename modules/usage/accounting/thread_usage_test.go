package accounting

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestThreadUsageConcurrentObservationsAndDuplicateTerminals(t *testing.T) {
	totals := newTestUsage(t)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			thread := fmt.Sprint(i)
			for range 100 {
				observation := totals.Observation(thread, "", "gpt-5.5", "")
				var duplicates sync.WaitGroup
				for range 3 {
					duplicates.Go(func() {
						observation.Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
					})
				}
				duplicates.Wait()
			}
		})
	}
	workers.Wait()
	for i := range 8 {
		got, valid := totals.Snapshot(fmt.Sprint(i))
		if !valid || got.Usage != (inference.Usage{InputTokens: 1000, UncachedInputTokens: 400, OutputTokens: 300, ReasoningTokens: 200}) {
			t.Fatalf("thread %d: counts=%+v valid=%v", i, got, valid)
		}
	}
}
func TestThreadUsageHasNoLifetimeThreadLimitAndHandlesOverflow(t *testing.T) {
	totals := newTestUsage(t)
	for i := range 256 {
		totals.Observation(fmt.Sprint(i), "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 1})
	}
	totals.Observation("excess", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 100})
	if got, valid := totals.Snapshot("excess"); !valid || got.InputTokens != 100 {
		t.Fatal("lifetime thread count disabled usage reporting")
	}
	totals.Observation("0", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 2})
	if got, valid := totals.Snapshot("0"); !valid || got.InputTokens != 3 {
		t.Fatal("capacity lost or stopped an existing total", got, valid)
	}
	totals.Observation("1", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: ^uint64(0)})
	if _, valid := totals.Snapshot("1"); valid || totals.threads["1"].counts.InputTokens != 1 {
		t.Fatal("overflow published or replaced the retained total")
	}
	totals.Observation("1", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 1})
	if _, valid := totals.Snapshot("1"); valid {
		t.Fatal("overflowed thread resumed with an incomplete total")
	}
	totals.Close()
	totals.Observation("0", "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 1})
	if _, valid := totals.Snapshot("0"); valid || len(totals.threads) != 0 {
		t.Fatal("closed accumulator retained or admitted usage")
	}
	for _, thread := range []string{"", strings.Repeat("x", maxThreadIdentityBytes+1)} {
		fresh := newTestUsage(t)
		fresh.Observation(thread, "", "gpt-5.5", "").Observe(inference.Usage{InputTokens: 1})
		if len(fresh.threads) != 0 {
			t.Fatal("unbounded identity admitted")
		}
	}
}

func TestThreadUsageFastModelLabel(t *testing.T) {
	for _, tc := range []struct {
		name, requested, served, want string
	}{
		{"fast", "fast", "", "gpt-5.6-sol [fast]"},
		{"priority", "priority", "", "gpt-5.6-sol [fast]"},
		{"served priority", "", "priority", "gpt-5.6-sol [fast]"},
		{"downgraded", "fast", "default", "gpt-5.6-sol"},
		{"standard", "", "", "gpt-5.6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := newTestUsage(t)
			counts := inference.Usage{InputTokens: 100, UncachedInputTokens: 50, OutputTokens: 10, ServiceTier: tc.served}
			usage.Observation("root", "", "gpt-5.6-sol", tc.requested).Observe(counts)
			report, ok := usage.Snapshot("root")
			if !ok || report.Model != tc.want || !report.Cost.Known {
				t.Fatalf("report = %+v, ok=%v", report, ok)
			}
		})
	}
	usage := newTestUsage(t)
	counts := inference.Usage{InputTokens: 100, UncachedInputTokens: 50, OutputTokens: 10}
	for _, tier := range []string{"fast", "priority", "default"} {
		usage.Observation("root", "", "gpt-5.6-sol", tier).Observe(counts)
	}
	report, ok := usage.Snapshot("root")
	if !ok || report.Model != "gpt-5.6-sol [fast], gpt-5.6-sol" {
		t.Fatalf("mixed-tier labels = %q, ok=%v", report.Model, ok)
	}
}
