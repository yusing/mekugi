package sessioncontrol

import "slices"

// Submission contains host-operation metadata only. The adapter retains the
// payload by ID until no pending acknowledgement or commitment refers to it.
// Source: apps/mekugi/internal/router/app_server_input_queue.go@dd97276b.
type Submission struct {
	ID, Turn                      string
	Committed, Ended, Interrupted bool
}

type InputAction uint8

const (
	InputAccepted InputAction = iota
	InputDeferred
	InputRejected
	InputWithdrawn
	InputSettled
	InputStale
)

type SteerAction uint8

const (
	SteersNone SteerAction = iota
	SteersRestore
	SteersRestoreAll
	SteersRequeue
	SteersUnconfirmed
)

type SteerResult struct {
	IDs    []string
	Action SteerAction
}

// InputResult names synchronous composer effects without retaining payloads or
// callbacks. KeepWaiting applies only to a withdrawn start during compaction's
// prerequisite interruption; its queued command must survive that withdrawal.
type InputResult struct {
	Submission  Submission
	Action      InputAction
	KeepWaiting bool
	Steers      SteerResult
}

func (h *Live) Submission() Submission { return h.submission }
func (h *Live) PendingStart() string   { return h.pendingStart }
func (h *Live) Steers() []string       { return h.steers }
func (h *Live) InputPending() bool     { return h.submission.ID != "" }
func (h *Live) Submit(id, turn string) {
	h.submission = Submission{ID: id, Turn: turn}
	if turn == "" {
		h.pendingStart, h.AwaitingTurn = id, true
		h.startOrigin = id
	}
}

func (h *Live) withdraw(s Submission, action InputAction) InputResult {
	if s.Turn == "" {
		if h.startOrigin == s.ID {
			h.startOrigin = ""
		}
		h.AwaitingTurn, h.pendingStart = false, ""
		h.Interruption.BeforeStart = false
	}
	return InputResult{Submission: s, Action: action,
		KeepWaiting: s.Interrupted && h.Compaction.PreservesWaitingInput()}
}

// SubmissionFailed handles transport rejection, before host acknowledgement.
func (h *Live) SubmissionFailed() InputResult {
	s := h.submission
	h.submission = Submission{}
	return h.withdraw(s, InputRejected)
}

// AcknowledgeInput preserves the separate host commitment and turn-end gates.
// A failed question reply restores immediately rather than being expedited.
func (h *Live) AcknowledgeInput(failed, questionReply, waitingSteer bool) InputResult {
	s := h.submission
	h.submission = Submission{}
	r := InputResult{Submission: s, Action: InputAccepted}
	if failed && questionReply {
		return h.withdraw(s, InputRejected)
	}
	if s.Turn == "" && s.Ended && !s.Committed {
		return h.withdraw(s, InputWithdrawn)
	}
	if s.Ended {
		if !s.Committed {
			h.steers = append(h.steers, s.ID)
		}
		r.Action, r.Steers = InputSettled, h.settleSteers(s.Interrupted, waitingSteer)
		return r
	}
	if failed && h.SendSteersAfterInterrupt && s.Turn == h.Interruption.Target && !s.Committed {
		h.steers = append(h.steers, s.ID)
		r.Action = InputDeferred
		return r
	}
	if failed {
		return h.withdraw(s, InputRejected)
	}
	switch {
	case s.Turn == "" || s.Committed:
	case s.Turn == h.Turn:
		h.steers = append(h.steers, s.ID)
	default:
		r.Action = InputStale
	}
	return r
}

// CommitInput receives a canonical submission ID. The adapter resolves payload
// text only for host versions whose committed user messages omit that identity.
func (h *Live) CommitInput(id string) {
	if id == "" {
		return
	}
	if h.pendingStart == id {
		h.pendingStart = ""
	}
	if h.submission.ID == id {
		h.submission.Committed = true
		return
	}
	if i := slices.Index(h.steers, id); i >= 0 {
		h.steers = slices.Delete(h.steers, i, i+1)
	}
}

func (h *Live) SettleInput(turn string, interrupted, waitingSteer bool) []InputResult {
	var results []InputResult
	if interrupted && h.pendingStart != "" {
		if h.submission.ID == h.pendingStart {
			h.submission.Ended, h.submission.Interrupted = true, true
			return nil
		}
		results = append(results, h.withdraw(Submission{ID: h.pendingStart, Interrupted: true}, InputWithdrawn))
	}
	if h.submission.Turn == turn {
		h.submission.Ended, h.submission.Interrupted = true, interrupted
		return results
	}
	return append(results, InputResult{Action: InputSettled, Steers: h.settleSteers(interrupted, waitingSteer)})
}

func (h *Live) settleSteers(interrupted, waitingSteer bool) SteerResult {
	r := SteerResult{IDs: h.steers}
	h.steers = nil
	sendNow := interrupted && h.SendSteersAfterInterrupt
	h.SendSteersAfterInterrupt = false
	switch {
	case interrupted && h.Compaction.PreservesWaitingInput():
		r.Action = SteersRestore
	case sendNow && (len(r.IDs) > 0 || waitingSteer):
		r.Action = SteersRequeue
	case interrupted:
		r.Action = SteersRestoreAll
	case len(r.IDs) > 0:
		r.Action = SteersUnconfirmed
	}
	return r
}
