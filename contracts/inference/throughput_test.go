package inference

import (
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
