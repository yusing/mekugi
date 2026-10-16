package sessioncontrol

// Live owns Main's admitted operations. Thread and Turn are host-observed
// identities; composer payloads and presentation never live in this state.
// Source: apps/mekugi/internal/router/app_server_lifecycle.go@dd97276b.
type Live struct {
	Thread, Turn             string
	Compaction               Compaction
	Settings                 Settings
	Replacement              Replacement
	Shell                    Shell
	Interruption             Interruption
	AwaitingTurn             bool
	SendSteersAfterInterrupt bool
	SteerInterruptAckPending bool
	ResumeEffort             bool
	submission               Submission
	pendingStart             string
	steers                   []string
}

func (h *Live) Starting() bool {
	return h.AwaitingTurn || h.Compaction.Starting() || h.Shell.phase == shellStarting
}

func (h *Live) Started(turn string) {
	h.Turn, h.AwaitingTurn = turn, false
	h.Compaction.Started(turn)
	h.Shell.started(turn)
}

func (h *Live) Completed(success bool) {
	h.Compaction.Completed(h.Turn, success)
	h.Turn, h.AwaitingTurn = "", false
	h.Shell.phase = shellIdle
	h.Interruption.Finish()
}

func (h *Live) CompactResponse(failed bool) {
	h.Compaction.Acknowledge(failed)
	if failed {
		h.AwaitingTurn = false
		h.Interruption.BeforeStart = false
	}
}

// AcceptsInput is the host gate. Questions and history restoration retain
// their own owners and are checked by the adapter before this gate.
func (h *Live) AcceptsInput() bool {
	return h.Thread != "" && !h.Replacement.Pending() && !h.Starting() &&
		!h.InputPending() && !h.SteerInterruptAckPending && !h.Compaction.Pending() &&
		!h.Settings.BlocksInput() && !h.Shell.BlocksInput(h.Turn) &&
		(h.Turn == "" || h.Turn != h.Interruption.Target)
}

func (h *Live) AcceptsShell() bool {
	return !h.Replacement.Pending() && !h.Shell.Pending && h.Shell.origin == nil &&
		!h.Starting() && !h.Compaction.Pending() && !h.InputPending() &&
		h.Interruption.Target == "" && !h.SteerInterruptAckPending
}

func (h *Live) AcceptsSettings() bool {
	return h.Thread != "" && !h.Starting() && !h.Replacement.Pending()
}

func (h *Live) Busy(waitingInput bool) bool {
	return h.Turn != "" || h.Starting() || h.Compaction.Pending() ||
		h.InputPending() || h.Settings.Pending() || h.Shell.Pending || waitingInput
}

// LeaveThread retires source operations together after confirmed replacement.
// Target restoration intent survives; queued payloads remain with the composer.
func (h *Live) LeaveThread() {
	*h = Live{Thread: h.Thread, ResumeEffort: h.ResumeEffort}
}

func (h *Live) TakeResumeEffort() bool {
	needed := h.ResumeEffort
	h.ResumeEffort = false
	return needed
}

type Interruption struct {
	Target      string
	BeforeStart bool
}

func (o *Interruption) Finish() { *o = Interruption{} }

// Interrupt names one request and the immediate composer consequence, if any.
// An empty Turn issues no host request. Pending retains deferred cancellation.
type Interrupt struct {
	Turn, Label             string
	RestoreWaiting, Pending bool
}

func (h *Live) PrepareEscape(hasUnsentSteer bool) bool {
	if h.SendSteersAfterInterrupt || h.SteerInterruptAckPending {
		return false
	}
	if h.Turn != "" && h.Interruption.Target == "" && !h.Compaction.Pending() {
		h.SendSteersAfterInterrupt = len(h.steers) > 0 ||
			h.submission.Turn != "" && !h.submission.Committed || hasUnsentSteer
	}
	return true
}

func (h *Live) Interrupt() Interrupt {
	r := Interrupt{Turn: h.Turn}
	if h.Compaction.Cancel() {
		r.RestoreWaiting = true
		r.Turn = h.Compaction.InterruptTarget()
		if r.Turn == "" {
			r.Pending = h.Compaction.Starting()
			return r
		}
	}
	if r.Turn == "" {
		if h.Starting() || h.InputPending() {
			h.Interruption.BeforeStart, r.Pending = true, true
		} else {
			r.RestoreWaiting = true
		}
		return r
	}
	if h.Interruption.Target == r.Turn {
		r.Turn = ""
		return r
	}
	h.Interruption.Target = r.Turn
	r.Label = "turn/interrupt"
	if h.Compaction.PreservesWaitingInput() {
		r.Label = "compact/interrupt"
	} else if h.SendSteersAfterInterrupt {
		h.SteerInterruptAckPending = true
		r.Label = "steer/interrupt"
	}
	return r
}

// InterruptResponse returns whether waiting composer input must be restored.
func (h *Live) InterruptResponse(label string, failed bool) bool {
	switch label {
	case "compact/interrupt":
		h.Compaction.Acknowledge(failed)
	case "steer/interrupt":
		h.SteerInterruptAckPending = false
	}
	if !failed {
		return false
	}
	h.Interruption.Finish()
	if label != "compact/interrupt" {
		h.SendSteersAfterInterrupt = false
	}
	return label == "compact/interrupt" || label == "steer/interrupt" && h.Turn == ""
}

type shellPhase uint8

const (
	shellIdle shellPhase = iota
	shellStarting
	shellRunning
)

type Shell struct {
	Pending bool
	origin  *string
	phase   shellPhase
}

func (o *Shell) Begin(turn string) {
	o.Pending, o.origin = true, new(turn)
	if turn == "" {
		o.phase = shellStarting
	}
}
func (o *Shell) Standalone() bool { return o.phase != shellIdle }
func (o *Shell) started(turn string) {
	if o.origin != nil && turn != *o.origin {
		o.phase, o.origin = shellRunning, nil
	} else if o.phase == shellStarting {
		o.phase = shellRunning
	}
}
func (o *Shell) Acknowledge(failed bool, turn string) {
	o.Pending = false
	if failed {
		o.Identified()
		if turn == "" {
			o.phase = shellIdle
		}
	}
}
func (o *Shell) Identified() { o.origin = nil }
func (o *Shell) BlocksInput(turn string) bool {
	return o.Pending || o.Standalone() || o.origin != nil && turn != *o.origin
}

type settingsPhase uint8

const (
	settingsIdle settingsPhase = iota
	settingsThread
	settingsTurn
)

type Settings struct {
	phase         settingsPhase
	Change        map[string]any
	turn          string
	RestoreEffort bool
}

func (o *Settings) Pending() bool     { return o.phase != settingsIdle }
func (o *Settings) BlocksInput() bool { return o.Pending() || o.RestoreEffort }
func (o *Settings) Begin(change map[string]any, turn string) {
	o.phase, o.Change, o.turn = settingsThread, change, turn
}
func (o *Settings) BeginLive()      { o.phase = settingsTurn }
func (o *Settings) Finish()         { o.phase, o.Change, o.turn = settingsIdle, nil, "" }
func (o *Settings) RestoredEffort() { o.RestoreEffort = false }

// Confirmed consumes an authoritative thread update. A live update may target
// only the same turn that received the intent; a later turn must not inherit it.
func (o *Settings) Confirmed(turn string) (change map[string]any, target string) {
	if !o.Pending() || o.Change == nil {
		return nil, ""
	}
	change, o.Change = o.Change, nil
	if o.turn != "" && o.turn == turn {
		return change, o.turn
	}
	o.Finish()
	return change, ""
}

type replacementPhase uint8

const (
	replacementIdle replacementPhase = iota
	replacementClear
	replacementResume
	replacementArchive
)

type Replacement struct {
	phase  replacementPhase
	Target string
}

func (o *Replacement) Pending() bool   { return o.phase != replacementIdle }
func (o *Replacement) Archiving() bool { return o.phase == replacementArchive }
func (o *Replacement) Clear()          { *o = Replacement{phase: replacementClear} }
func (o *Replacement) Resume(thread string) {
	*o = Replacement{phase: replacementResume, Target: thread}
}
func (o *Replacement) Archive() { *o = Replacement{phase: replacementArchive} }
func (o *Replacement) Finish()  { *o = Replacement{} }
