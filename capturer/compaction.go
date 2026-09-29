package capturer

import "context"

// CompactionMetrics contains only routing provenance and content-free counts.
// Router answers have no provider attempt; zero usage is not a saving estimate.
type CompactionMetrics struct {
	CompactionAnswer       string  `json:"compaction_answer,omitempty"`
	CompactionSummaryBytes *uint64 `json:"compaction_summary_bytes,omitempty"`
	CompactionChanges      *uint64 `json:"compaction_changes,omitempty"`
	CompactionFailures     *uint64 `json:"compaction_failures,omitempty"`
}

func ObserveCompaction(ctx context.Context, answer string, summaryBytes, changes, failures int) {
	if answer != "router" && answer != "provider" || summaryBytes < 0 || changes < 0 || failures < 0 {
		return
	}
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.requestKind != "compaction" {
		return
	}
	state.compaction = CompactionMetrics{CompactionAnswer: answer}
	if answer == "router" {
		state.compaction.CompactionSummaryBytes = new(uint64(summaryBytes))
		state.compaction.CompactionChanges = new(uint64(changes))
		state.compaction.CompactionFailures = new(uint64(failures))
	}
}
