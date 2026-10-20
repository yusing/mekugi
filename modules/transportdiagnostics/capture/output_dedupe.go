// Source: capturer/output_dedupe.go@b5162407.
package capture

import "context"

// OutputDeduplicationMetrics counts request projections across this launch.
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
	m := &r.metrics.OutputDeduplication
	m.Projections++
	m.Occurrences += occurrences
	if !measured {
		m.Unmeasured++
		return
	}
	m.SavedTokens += savedTokens
}
