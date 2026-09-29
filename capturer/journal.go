package capturer

import (
	"context"
	"maps"
)

// JournalMetrics is a cumulative, content-free snapshot for the owning thread.
// Consumers select its newest sequence, never sum snapshots across exchanges.
type JournalMetrics struct {
	StartedAt          string            `json:"started_at"`
	Sequence           uint64            `json:"sequence"`
	Operations         map[string]uint64 `json:"operations"`
	StandaloneRequests uint64            `json:"standalone_requests"`
	FinalAnswers       uint64            `json:"final_answers"`
	FinalAnswerBytes   uint64            `json:"final_answer_bytes"`
	EmptyOutcomes      uint64            `json:"empty_outcomes"`
	LastOutcomeEmpty   *bool             `json:"last_outcome_empty,omitempty"`
}

func (j *JournalMetrics) Clone() *JournalMetrics {
	if j == nil {
		return nil
	}
	clone := *j
	clone.Operations = maps.Clone(j.Operations)
	if j.LastOutcomeEmpty != nil {
		clone.LastOutcomeEmpty = new(*j.LastOutcomeEmpty)
	}
	return &clone
}

func ObserveJournal(ctx context.Context, metrics *JournalMetrics) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.journal = metrics.Clone()
}
