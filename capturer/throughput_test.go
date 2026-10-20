package capturer

import (
	"reflect"
	"testing"
	"time"
)

func TestOutputThroughputRate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		measurement OutputThroughput
		wantRate    float64
		wantKnown   bool
	}{
		{name: "absent"},
		{name: "missing-duration", measurement: OutputThroughput{OutputTokens: 100, MeasuredRequests: 1}},
		{name: "missing-request-count", measurement: OutputThroughput{OutputTokens: 100, DurationNanos: uint64(time.Second)}},
		{name: "measured-zero-output", measurement: OutputThroughput{DurationNanos: uint64(time.Second), MeasuredRequests: 1}, wantKnown: true},
		{name: "fractional-second", measurement: OutputThroughput{OutputTokens: 30, DurationNanos: uint64(500 * time.Millisecond), MeasuredRequests: 1}, wantRate: 60, wantKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotRate, gotKnown := tc.measurement.Rate()
			if gotRate != tc.wantRate || gotKnown != tc.wantKnown {
				t.Fatalf("Rate() = (%g, %t), want (%g, %t)", gotRate, gotKnown, tc.wantRate, tc.wantKnown)
			}
		})
	}
}

func TestOutputThroughputAddWeightsByMeasuredDuration(t *testing.T) {
	var total OutputThroughput
	total.Add(OutputThroughput{OutputTokens: 100, DurationNanos: uint64(time.Second), MeasuredRequests: 1})
	total.Add(OutputThroughput{OutputTokens: 90, DurationNanos: uint64(9 * time.Second), MeasuredRequests: 1})
	total.Add(OutputThroughput{})
	want := OutputThroughput{OutputTokens: 190, DurationNanos: uint64(10 * time.Second), MeasuredRequests: 2}
	if total != want {
		t.Fatalf("aggregate = %+v, want %+v", total, want)
	}
	// The two individual rates are 100 and 10 TPS. Their arithmetic mean,
	// 55 TPS, is not the aggregate's duration-weighted 19 TPS.
	if rate, known := total.Rate(); !known || rate != 19 {
		t.Fatalf("aggregate Rate() = (%g, %t), want (19, true)", rate, known)
	}
}

func TestSnapshotOutputThroughputAggregatesMeasuredAttempts(t *testing.T) {
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	first := OutputThroughput{OutputTokens: 100, DurationNanos: uint64(time.Second), MeasuredRequests: 1}
	second := OutputThroughput{OutputTokens: 90, DurationNanos: uint64(9 * time.Second), MeasuredRequests: 1}
	zero := OutputThroughput{DurationNanos: uint64(2 * time.Second), MeasuredRequests: 1}
	r.addExchange(captureRecord{RequestSequence: 1}, []captureRecord{
		{ProviderAttempt: 1, Usage: &ProviderUsage{OutputTokens: 100, OutputThroughput: first}},
		{ProviderAttempt: 2, Usage: &ProviderUsage{OutputTokens: 90, OutputThroughput: second}},
		// Authoritative usage without timing stays in ordinary token totals,
		// but must not inflate the numerator of the paired measurement.
		{ProviderAttempt: 3, Usage: &ProviderUsage{OutputTokens: 1000}},
		{ProviderAttempt: 4},
	})
	r.addExchange(captureRecord{RequestSequence: 2}, []captureRecord{
		{ProviderAttempt: 1, Usage: &ProviderUsage{OutputThroughput: zero}},
	})
	s := r.Snapshot()
	want := OutputThroughput{OutputTokens: 190, DurationNanos: uint64(12 * time.Second), MeasuredRequests: 3}
	if s.Usage.OutputThroughput != want || s.Usage.OutputTokens != 1190 {
		t.Fatalf("snapshot usage = %+v, want paired measurement %+v and 1190 total output tokens", s.Usage, want)
	}
	if rate, known := s.Usage.OutputThroughput.Rate(); !known || rate != float64(190)/12 {
		t.Fatalf("snapshot Rate() = (%g, %t), want (%g, true)", rate, known, float64(190)/12)
	}
	if s.Requests.Retries != 3 || s.Usage.MissingAttempts != 1 {
		t.Fatalf("retry/missing evidence = requests %+v, usage %+v", s.Requests, s.Usage)
	}
	if got := s.Exchanges[0].Usage.OutputThroughput; got != (OutputThroughput{OutputTokens: 190, DurationNanos: uint64(10 * time.Second), MeasuredRequests: 2}) {
		t.Fatalf("exchange throughput = %+v", got)
	}
	for i, want := range []OutputThroughput{first, second, {}} {
		if got := s.Exchanges[0].ProviderAttempts[i].Usage.OutputThroughput; got != want {
			t.Fatalf("attempt %d throughput = %+v, want %+v", i+1, got, want)
		}
	}
	if rate, known := s.Exchanges[1].Usage.OutputThroughput.Rate(); !known || rate != 0 {
		t.Fatalf("zero-output exchange Rate() = (%g, %t), want (0, true)", rate, known)
	}
	baseline := r.Snapshot()
	s.Usage.OutputThroughput.OutputTokens = 9999
	s.Exchanges[0].Usage.OutputThroughput.DurationNanos = 9999
	s.Exchanges[0].ProviderAttempts[0].Usage.OutputThroughput.MeasuredRequests = 9999
	if got := r.Snapshot(); !reflect.DeepEqual(got, baseline) {
		t.Fatal("mutating snapshot throughput leaked into recorder")
	}
}

func TestSnapshotOutputThroughputSurvivesDetailEviction(t *testing.T) {
	r, err := New(Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	measurement := OutputThroughput{OutputTokens: 7, DurationNanos: uint64(2 * time.Second), MeasuredRequests: 1}
	r.addExchange(captureRecord{RequestSequence: 1}, []captureRecord{
		{ProviderAttempt: 1, Usage: &ProviderUsage{OutputTokens: 7, OutputThroughput: measurement}},
	})
	for i := range maxRetainedExchangeDetails {
		r.addExchange(captureRecord{RequestSequence: uint64(i + 2)}, []captureRecord{
			{ProviderAttempt: 1, Usage: &ProviderUsage{OutputTokens: 100}},
		})
	}
	s := r.Snapshot()
	if len(s.Exchanges) != maxRetainedExchangeDetails || s.Exchanges[0].Sequence != 2 || s.Capture.DroppedExchangeDetails != 1 {
		t.Fatalf("detail eviction = count %d, first sequence %d, dropped %d", len(s.Exchanges), s.Exchanges[0].Sequence, s.Capture.DroppedExchangeDetails)
	}
	if got := s.Usage.OutputThroughput; got != measurement {
		t.Fatalf("cumulative throughput after measured detail eviction = %+v, want %+v", got, measurement)
	}
	if rate, known := s.Usage.OutputThroughput.Rate(); !known || rate != 3.5 {
		t.Fatalf("retained aggregate Rate() = (%g, %t), want (3.5, true)", rate, known)
	}
}
