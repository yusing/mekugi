package capturer

// OutputThroughput pairs authoritative output tokens with the elapsed provider
// request time for exactly the same measured requests. Zero duration is absent
// timing, not zero throughput. It excludes unmeasured requests from both sums.
type OutputThroughput struct {
	OutputTokens     uint64 `json:"output_tokens"`
	DurationNanos    uint64 `json:"duration_ns"`
	MeasuredRequests uint64 `json:"measured_requests"`
}

func (t OutputThroughput) Rate() (float64, bool) {
	if t.DurationNanos == 0 || t.MeasuredRequests == 0 {
		return 0, false
	}
	return float64(t.OutputTokens) * 1e9 / float64(t.DurationNanos), true
}

func (t *OutputThroughput) Add(next OutputThroughput) {
	t.OutputTokens += next.OutputTokens
	t.DurationNanos += next.DurationNanos
	t.MeasuredRequests += next.MeasuredRequests
}
