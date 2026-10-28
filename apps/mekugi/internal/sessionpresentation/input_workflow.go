package sessionpresentation

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// InputSelection is a prepared batch or a prerequisite host interruption.
type InputSelection struct {
	Batch            conversationmodel.InputBatch
	Steer, Interrupt bool
	Continue         bool
}

// NextInput runs after application restoration and guard admission. Acquisition
// may defer the returned batch through Conversation.ReturnBatch.
// Source: apps/mekugi/internal/router/app_server_input_queue.go:114:155@25e285b1.
func (m *Model) NextInput(control *sessioncontrol.Live, conversation *conversationmodel.Session, emitting bool) InputSelection {
	var next InputSelection
	if m.replay || !control.AcceptsInput() {
		return next
	}
	compactSteer := len(conversation.Unsent) > 0 && conversation.Unsent[0].Text == "/compact" && !conversation.Unsent[0].Compact.WaitForIdle
	if conversation.Questions.Waiting(control.Thread) && !compactSteer {
		return next
	}
	if control.Turn == "" {
		next.Continue = control.Compaction.ContinuationPending() && len(conversation.Unsent)+len(conversation.Queued) == 0 || control.Shell.Continuation()
	}
	next.Steer = control.Turn != ""
	if len(conversation.Unsent) > 0 && next.Steer {
		if conversation.Unsent[0].Question != nil && emitting && !conversation.AdvancePrompt() {
			return next
		}
		if conversation.Unsent[0].Text == "/compact" {
			next.Interrupt = control.Compaction.InterruptBefore(sessioncontrol.CompactIntent(conversation.Unsent[0].Compact), control.Turn)
			return next
		}
	}
	next.Batch = conversation.TakeBatch(next.Steer || control.LocalStop != "")
	return next
}

func (m *Model) StageInput(control *sessioncontrol.Live, conversation *conversationmodel.Session, id string, submission conversationmodel.Submission, steer bool) {
	turn := ""
	if steer {
		turn = control.Turn
	}
	conversation.Stage(id, submission)
	control.Submit(id, turn)
	m.Status.Set("Sending…", false)
}

func (m *Model) PruneInput(control *sessioncontrol.Live, conversation *conversationmodel.Session) {
	conversation.Retain(append([]string{control.Submission().ID, control.PendingStart()}, control.Steers()...))
}

type InputEffects struct {
	History          []string
	Restored         conversationmodel.InputRestoration
	Withdrawn        string
	Notice           string
	NoticeError      bool
	InterruptedStart bool
}

func (m *Model) AcknowledgeInput(control *sessioncontrol.Live, conversation *conversationmodel.Session, method, failure string, failed bool) InputEffects {
	defer m.PruneInput(control, conversation)
	submission := conversation.Submitted[control.Submission().ID]
	var remembered []string
	if !failed {
		// Empty continuation produces no userMessage notification to commit it.
		if method == "turn/start" && len(submission.Parts) == 0 {
			m.CommitInput(control, conversation, control.Submission().ID, "")
		}
		remembered = conversation.Acknowledged(control.Submission().ID)
	}
	result := control.AcknowledgeInput(failed, len(submission.Parts) == 1 && submission.Parts[0].Question != nil, len(conversation.Unsent) > 0)
	if result.Action == sessioncontrol.InputRejected {
		m.Status.Set(method+": "+failure, true)
	}
	effects := m.ApplyInputResult(control, conversation, result)
	effects.History = remembered
	return effects
}

// ApplyInputResult settles payloads before publishing renderer consequences.
// Source: apps/mekugi/internal/router/app_server_input_queue.go:230:331@25e285b1.
func (m *Model) ApplyInputResult(control *sessioncontrol.Live, conversation *conversationmodel.Session, result sessioncontrol.InputResult) InputEffects {
	var effects InputEffects
	submission := conversation.Submitted[result.Submission.ID]
	switch result.Action {
	case sessioncontrol.InputAccepted, sessioncontrol.InputStale:
		if m.Status.Text == "Sending…" && !m.Status.Alert {
			if control.Turn != "" {
				m.Status.SetText("Working")
			} else if control.Starting() {
				m.Status.SetText("Starting turn…")
			}
		}
		if result.Action == sessioncontrol.InputStale {
			effects.Restored = conversation.RestoreInput(submission.Parts...)
			effects.Notice = "Turn ended before the steer · restored to the composer"
		}
	case sessioncontrol.InputRejected, sessioncontrol.InputWithdrawn:
		parts := submission.Parts
		if result.Submission.Turn == "" {
			effects.Withdrawn = result.Submission.ID
			effects.InterruptedStart = result.Submission.Interrupted
			if !result.KeepWaiting {
				parts = slices.Concat(parts, conversation.TakeWaiting())
			}
		}
		effects.Restored = conversation.RestoreInput(parts...)
	case sessioncontrol.InputSettled:
		parts := conversation.Parts(result.Steers.IDs)
		switch result.Steers.Action {
		case sessioncontrol.SteersRestore:
			effects.Restored = conversation.RestoreInput(parts...)
		case sessioncontrol.SteersRequeue:
			conversation.Unsent = slices.Concat(parts, conversation.Unsent)
		case sessioncontrol.SteersRestoreAll:
			effects.Restored = conversation.RestoreInput(slices.Concat(parts, conversation.TakeWaiting())...)
		case sessioncontrol.SteersUnconfirmed:
			effects.Restored = conversation.RestoreInput(parts...)
			effects.Notice = "Unconfirmed steer restored to the composer"
		}
	}
	return effects
}

func (m *Model) CommitInput(control *sessioncontrol.Live, conversation *conversationmodel.Session, id, text string) {
	defer m.PruneInput(control, conversation)
	if id != "" {
		control.CommitInput(id)
		return
	}
	for _, matched := range conversation.MatchSubmitted(text, control.PendingStart(), control.Submission().ID, control.Steers()) {
		control.CommitInput(matched)
	}
}

func (m *Model) SettleInput(control *sessioncontrol.Live, conversation *conversationmodel.Session, turn string, interrupted bool) []InputEffects {
	defer m.PruneInput(control, conversation)
	var effects []InputEffects
	for _, result := range control.SettleInput(turn, interrupted, len(conversation.Unsent) > 0) {
		effects = append(effects, m.ApplyInputResult(control, conversation, result))
	}
	return effects
}

func (m *Model) RestoreInterruptedInput(conversation *conversationmodel.Session, intent sessioncontrol.Interrupt) conversationmodel.InputRestoration {
	var restored conversationmodel.InputRestoration
	if intent.RestoreWaiting {
		restored = conversation.RestoreInput(conversation.TakeWaiting()...)
	}
	if intent.Pending || intent.Turn != "" {
		m.Status.SetText("Interrupting…")
	}
	return restored
}

// SettleWithdrawnInput receives retained conversation presence, not paint state.
func (m *Model) SettleWithdrawnInput(effects InputEffects, hasContent bool) {
	if effects.InterruptedStart && !hasContent {
		m.Status.Started = time.Time{}
		m.Status.SetText("Ready")
	}
}
