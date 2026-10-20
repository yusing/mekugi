package sessioncontrol

// CompactIntent travels with the composer's queued command, not its text or
// attachments. Continuation is requested only when Main was busy at admission.
type CompactIntent struct {
	WaitForIdle bool
	Continue    bool
}

type compactPhase uint8

const (
	compactIdle compactPhase = iota
	compactInterrupting
	compactStarting
	compactRunning
	compactCompleted
)

// Compaction owns the manual workflow, including the prerequisite interrupt.
// Acknowledgements and matching turn completion are independent gates. Its zero
// value is idle; replacing the session discards this live state, never replays it.
// Source: apps/mekugi/internal/router/app_server_lifecycle.go:134:186@b4e9e2fb
// and app_server_commands.go:81:118, app_server_ui.go:1670:1706@b4e9e2fb.
type Compaction struct {
	phase        compactPhase
	turn         string
	ackPending   bool
	continueTask bool
	cancelled    bool
}

// InterruptBefore admits one prerequisite interrupt while retaining the queued
// command. Tab waits for natural completion instead.
func (c *Compaction) InterruptBefore(intent CompactIntent, turn string) bool {
	if intent.WaitForIdle || turn == "" || c.Pending() {
		return false
	}
	*c = Compaction{phase: compactInterrupting, turn: turn, ackPending: true}
	return true
}

func (c *Compaction) Begin(intent CompactIntent) {
	*c = Compaction{phase: compactStarting, ackPending: true, continueTask: intent.Continue}
}

func (c *Compaction) Started(turn string) {
	if c.phase == compactStarting {
		c.phase, c.turn = compactRunning, turn
	}
}

func (c *Compaction) Completed(turn string, success bool) {
	if c.turn != turn || turn == "" {
		return
	}
	c.turn = ""
	if c.phase == compactRunning {
		c.phase = compactCompleted
		if !success {
			c.continueTask = false
		}
	}
}

func (c *Compaction) Acknowledge(failed bool) {
	c.ackPending = false
	if failed {
		*c = Compaction{}
	}
}

func (c *Compaction) Starting() bool { return c.phase == compactStarting }
func (c *Compaction) Active() bool {
	return c.phase == compactStarting || c.phase == compactRunning
}
func (c *Compaction) Pending() bool { return c.ackPending || c.Active() || c.turn != "" }
func (c *Compaction) AcknowledgementPending() bool {
	return c.phase != compactInterrupting && c.ackPending
}
func (c *Compaction) PreservesWaitingInput() bool {
	return c.phase == compactInterrupting && !c.cancelled
}
func (c *Compaction) ContinuationPending() bool { return c.continueTask }

// CancelQueued restores local intent without interrupting Main. An already
// issued prerequisite interrupt still needs its acknowledgement and completion.
func (c *Compaction) CancelQueued(hasQueued bool) (cancelled, interruptionIssued bool) {
	if !hasQueued || c.Active() || c.AcknowledgementPending() {
		return false, false
	}
	interruptionIssued = c.PreservesWaitingInput()
	c.cancelled, c.continueTask = true, false
	return true, interruptionIssued
}

// Cancel admits cancellation of the compact operation, including completion
// racing its acknowledgement. The caller restores waiting composer payloads.
func (c *Compaction) Cancel() bool {
	if !c.Active() && !c.AcknowledgementPending() && !c.continueTask {
		return false
	}
	c.cancelled, c.continueTask = true, false
	return true
}

// InterruptTarget becomes available after a cancelled start receives its host
// turn identity. Completion clears it so cancellation cannot hit a later turn.
func (c *Compaction) InterruptTarget() string {
	if c.cancelled && c.phase == compactRunning {
		return c.turn
	}
	return ""
}

// Continue consumes eligibility once. Waiting user input takes precedence over
// the single visible continuation; idle or cancelled compaction stays idle.
func (c *Compaction) Continue(waitingInput bool) string {
	if c.Pending() || !c.continueTask {
		return ""
	}
	c.continueTask = false
	if waitingInput {
		return ""
	}
	return "Continue the task from where you left off before compaction."
}
