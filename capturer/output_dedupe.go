package capturer

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
func ObserveOutputDeduplication(ctx context.Context, before, after []byte, occurrences uint64) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	r := state.recorder
	original, err1 := contentTokens(before, r.codec)
	projected, err2 := contentTokens(after, r.codec)
	r.mu.Lock()
	defer r.mu.Unlock()
	m := &r.metrics.OutputDeduplication
	m.Projections++
	m.Occurrences += occurrences
	if err1 != nil || err2 != nil {
		m.Unmeasured++
		return
	}
	m.SavedTokens += int64(original - projected)
}
