package accounting

import (
	"time"

	"github.com/yusing/mekugi/contracts/inference"
)

// The current round is the latest started forwarded provider request, not an
// app-server turn. Persist its start ordering so late older responses cannot
// overwrite it. Keep the last valid rate visible until a newer sample arrives.
type RoundOutput struct {
	StartedUnixNano int64
	Throughput      inference.OutputThroughput
}

func (o *Observation) Begin() {
	if o == nil || o.totals == nil {
		return
	}
	u := o.totals
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || o.thread == "" || len(o.thread) > maxThreadIdentityBytes {
		return
	}
	u.loadLocked(o.thread)
	o.started = time.Now()
	delta := newThreadUsageTotal()
	delta.roundOutput.StartedUnixNano = o.started.UnixNano()
	u.updateLocked(o.thread, delta)
}

func MeasureThroughput(counts inference.Usage, started, receivedAt time.Time) inference.OutputThroughput {
	if started.IsZero() || !counts.TotalsKnown || counts.Inconsistent {
		return inference.OutputThroughput{}
	}
	if elapsed := receivedAt.Sub(started); elapsed > 0 {
		return inference.OutputThroughput{OutputTokens: counts.OutputTokens, DurationNanos: uint64(elapsed), MeasuredRequests: 1}
	}
	return inference.OutputThroughput{}
}
