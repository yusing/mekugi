// Source: capturer/output_dedupe.go@b5162407.
package capture

import "context"

// OutputDeduplicationMetrics counts request projections in this launch.
// SavedTokens is a local net content estimate, including source anchors.
type OutputDeduplicationMetrics struct {
	Projections uint64 `json:"projections"`
	Occurrences uint64 `json:"occurrences"`
	SavedTokens int64  `json:"saved_tokens"`
	Unmeasured  uint64 `json:"unmeasured"`
}

// ObserveOutputDeduplication retains sizes only, never output content.
func ObserveOutputDeduplication(ctx context.Context, occurrences uint64, savedTokens int64, measured bool) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	r := state.recorder
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics.OutputDeduplication.add(occurrences, savedTokens, measured)
	if state.threadID == "" {
		return
	}
	if r.metrics.OutputDeduplicationThreads == nil {
		r.metrics.OutputDeduplicationThreads = make(map[string]OutputDeduplicationMetrics)
	}
	thread := r.metrics.OutputDeduplicationThreads[state.threadID]
	thread.add(occurrences, savedTokens, measured)
	r.metrics.OutputDeduplicationThreads[state.threadID] = thread
}

func (m *OutputDeduplicationMetrics) add(occurrences uint64, savedTokens int64, measured bool) {
	m.Projections++
	m.Occurrences += occurrences
	if !measured {
		m.Unmeasured++
		return
	}
	m.SavedTokens += savedTokens
}

// Add sums another set of receipts, such as several threads' totals.
func (m *OutputDeduplicationMetrics) Add(other OutputDeduplicationMetrics) {
	m.Projections += other.Projections
	m.Occurrences += other.Occurrences
	m.SavedTokens += other.SavedTokens
	m.Unmeasured += other.Unmeasured
}
