package presentation

import (
	"slices"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

// InputRestoration identifies presentation changes without renderer state.
type InputRestoration struct {
	Rejected []string
	Detached []Input
	Parked   bool
	Changed  bool
}

// RestoreInput restores Main without borrowing an active prompt's editor.
// Source: apps/mekugi/internal/router/app_server_input_queue.go:64:88@25e285b1.
func (m *Session) RestoreInput(parts ...Input) InputRestoration {
	var result InputRestoration
	var ordinary []Input
	for _, part := range parts {
		if call := part.Question; call != nil && !call.Sync {
			if call.Reject() {
				result.Rejected = append(result.Rejected, call.ID)
			}
		} else if part.Origin != "" {
			result.Detached = append(result.Detached, part)
		} else {
			ordinary = append(ordinary, part)
		}
	}
	result.Parked = m.Questions.Active != nil || m.Approvals.Open && len(m.Approvals.Pending) > 0 && m.Approvals.Pending[0].DenialChoice() >= 0
	if m.Questions.Active != nil {
		m.Questions.Confirm = false
	}
	if len(ordinary) == 0 {
		return result
	}
	result.Changed = true
	if result.Parked {
		var editor composer.Editor
		editor.Resume(m.PromptEditor.Parked)
		editor.Restore(Drafts(ordinary)...)
		m.PromptEditor.Parked = editor.Park()
	} else {
		m.Editor.Restore(Drafts(ordinary)...)
	}
	return result
}

func (m *Session) QueueInput(input Input, nextTurn bool) {
	input.NextTurn = nextTurn
	if nextTurn {
		m.Queued = append(m.Queued, input)
	} else {
		m.Unsent = append(m.Unsent, input)
	}
}

type InputBatch struct {
	Parts  []Input
	Unsent bool
}

func (m *Session) TakeBatch(steer bool) InputBatch {
	var batch InputBatch
	if len(m.Unsent) > 0 {
		batch.Parts, m.Unsent = Batch(m.Unsent)
		batch.Unsent = true
	} else if !steer {
		batch.Parts, m.Queued = Batch(m.Queued)
	}
	return batch
}

func (m *Session) ReturnBatch(batch InputBatch) {
	if batch.Unsent {
		m.Unsent = slices.Concat(batch.Parts, m.Unsent)
	} else {
		m.Queued = slices.Concat(batch.Parts, m.Queued)
	}
}

// Acknowledged records ordinary input once, independently of host commitment.
func (m *Session) Acknowledged(id string) []string {
	var remembered []string
	submission := m.Submitted[id]
	for i, part := range submission.Parts {
		if part.InHistory {
			continue
		}
		if part.Question == nil && part.ReplyCount == 0 {
			m.Editor.Remember(part.Draft)
			if part.Text != "" {
				remembered = append(remembered, part.Text)
			}
		}
		submission.Parts[i].InHistory = true
	}
	return remembered
}

// MatchSubmitted preserves ordered fallback for hosts omitting client identity.
func (m *Session) MatchSubmitted(text, start, sending string, steers []string) []string {
	var ids []string
	if start != "" && m.Submitted[start].Text == text {
		ids = append(ids, start)
	}
	for _, id := range append([]string{sending}, steers...) {
		if id != "" && m.Submitted[id].Text == text {
			return append(ids, id)
		}
	}
	return ids
}

// SteerQueued promotes ordinary queued input without replaying submitted input
// or turning a compaction command or question answer into a message.
func (m *Session) SteerQueued() bool {
	promoted := false
	m.Queued = slices.DeleteFunc(m.Queued, func(input Input) bool {
		if !steerable(input) {
			return false
		}
		input.NextTurn = false
		m.Unsent = append(m.Unsent, input)
		promoted = true
		return true
	})
	return promoted
}

func steerable(input Input) bool { return input.Question == nil && input.Text != "/compact" }
