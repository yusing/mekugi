package accounting

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestOutputThroughputCurrentRound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := newTestUsage(t)

		first := usage.Observation("main", "main", "gpt-6-sol", "")
		first.Begin()
		requireRoundThroughput(t, usage, "main", 0, false)
		time.Sleep(2 * time.Second)
		first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 80})
		requireRoundThroughput(t, usage, "main", 40, true)
		first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 999})
		first.Finish()
		if report := requireRoundThroughput(t, usage, "main", 40, true); report.Roundtrips != 1 {
			t.Fatal(report)
		}
		// Tool time between requests does not dilute the next provider round.
		time.Sleep(time.Minute)
		zero := usage.Observation("main", "main", "gpt-6-luna", "")
		zero.Begin()
		requireRoundThroughput(t, usage, "main", 40, true)
		time.Sleep(time.Second)
		zero.Observe(inference.Usage{TotalsKnown: true})
		requireRoundThroughput(t, usage, "main", 0, true)
		time.Sleep(time.Second)
		gap := usage.Observation("main", "main", "gpt-6-sol", "")
		gap.Begin()
		time.Sleep(time.Second)
		gap.Finish()
		requireRoundThroughput(t, usage, "main", 0, true)
	})
}

func TestOutputThroughputConcurrentRoundOrdering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := newTestUsage(t)

		old := usage.Observation("main", "main", "gpt-6-sol", "")
		old.Begin()
		time.Sleep(time.Second)
		next := usage.Observation("main", "main", "gpt-6-sol", "")
		next.Begin()
		time.Sleep(time.Second)
		next.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 60})
		requireRoundThroughput(t, usage, "main", 60, true)
		time.Sleep(time.Second)
		old.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 30})
		requireRoundThroughput(t, usage, "main", 60, true)
		// The older request still keeps its own paired measurement for capture.
		if rate, known := old.Throughput.Rate(); !known || rate != 10 {
			t.Fatalf("older capture rate=%g,%v", rate, known)
		}
	})
}

func TestOutputThroughputMissingAndPartialEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		time.Sleep(2 * time.Second)
		for _, tc := range []struct {
			name   string
			counts inference.Usage
			time   time.Time
			known  bool
			rate   float64
		}{
			{name: "missing-timing", counts: inference.Usage{TotalsKnown: true, OutputTokens: 80}},
			{name: "missing-totals", counts: inference.Usage{Incomplete: true}, time: started},
			{name: "inconsistent", counts: inference.Usage{TotalsKnown: true, OutputTokens: 80, Inconsistent: true}, time: started},
			{name: "partial-categories-known-totals", counts: inference.Usage{TotalsKnown: true, OutputTokens: 80, Incomplete: true}, time: started, known: true, rate: 40},
		} {
			got := MeasureThroughput(tc.counts, tc.time, time.Now())
			rate, known := got.Rate()
			if known != tc.known || rate != tc.rate {
				t.Fatalf("%s rate=%g,%v want %g,%v", tc.name, rate, known, tc.rate, tc.known)
			}
		}
		got := MeasureThroughput(inference.Usage{TotalsKnown: true, OutputTokens: 80}, time.Now(), time.Now())
		if _, known := got.Rate(); known {
			t.Fatal("zero elapsed time was a measured rate")
		}
	})
}

func requireRoundThroughput(t *testing.T, usage *Service, thread string, rate float64, known bool) Report {
	t.Helper()
	report, _ := usage.Snapshot(thread)
	got, ok := report.RoundOutput.Throughput.Rate()
	if got != rate || ok != known {
		t.Fatalf("%s rate=(%g,%v), want (%g,%v); %+v", thread, got, ok, rate, known, report)
	}
	return report
}
