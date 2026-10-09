package router

import (
	"fmt"
	"time"

	"github.com/yusing/mekugi/capturer"
)

// The current round is the latest started forwarded provider request, not an
// app-server turn. Persist its start ordering so late older responses cannot
// overwrite it. Keep the last valid rate visible until a newer sample arrives.
type providerRoundOutput struct {
	StartedUnixNano int64
	Throughput      capturer.OutputThroughput
}

func (o *threadUsageObservation) begin() {
	if o == nil || o.totals == nil {
		return
	}
	u := o.totals
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || o.thread == "" || len(o.thread) > maxActivityMetadataBytes {
		return
	}
	u.loadLocked(o.thread)
	o.started = time.Now()
	delta := newThreadUsageTotal()
	delta.roundOutput.StartedUnixNano = o.started.UnixNano()
	u.updateLocked(o.thread, delta)
}

func measureOutputThroughput(counts tokenCounts, started, receivedAt time.Time) capturer.OutputThroughput {
	if started.IsZero() || !counts.TotalsKnown || counts.Inconsistent {
		return capturer.OutputThroughput{}
	}
	if elapsed := receivedAt.Sub(started); elapsed > 0 {
		return capturer.OutputThroughput{OutputTokens: counts.OutputTokens, DurationNanos: uint64(elapsed), MeasuredRequests: 1}
	}
	return capturer.OutputThroughput{}
}

func outputThroughputLabel(throughput capturer.OutputThroughput) string {
	if rate, known := throughput.Rate(); known {
		return fmt.Sprintf("%.1f tok/s", rate)
	}
	return ""
}
