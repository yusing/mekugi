package sessioncontrol

import (
	"crypto/rand"

	"github.com/yusing/mekugi/contracts/observation"
)

// CompactIntent travels with the composer's queued command, not its text or
// attachments. Continuation is requested only when Main was busy at admission.
type CompactIntent struct {
	WaitForIdle bool
	Continue    bool
}

type compactPhase uint8

const (
	compactIdle compactPhase = iota
	compactStarting
	compactRunning
	compactCompleted
)

// Compaction owns the manual workflow, and its native lifecycle receipts.
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
	success      bool
	requestID    string
	resetDone    bool
	hostDone     bool
}

func (c *Compaction) Begin(intent CompactIntent) {
	*c = Compaction{phase: compactStarting, ackPending: true, continueTask: intent.Continue}
}

func (c *Compaction) Started(turn string) {
	if c.phase == compactStarting && (c.requestID == "" || turn == c.turn) {
		c.phase, c.turn = compactRunning, turn
	}
}

// NativeRequestID binds lifecycle receipts to this manual request, not a turn
// that may already contain an older automatic or compound reset.
func (c *Compaction) NativeRequestID() string {
	if c.requestID == "" {
		c.requestID = rand.Text()
	}
	return c.requestID
}

type ContextReset = observation.ContextResetEvent

func (c *Compaction) ObserveReset(event ContextReset) {
	if !c.Active() || c.requestID == "" || event.RequestID != c.requestID || event.Turn == "" {
		return
	}
	if event.Status == "accepted" && c.turn == "" {
		c.turn = event.Turn
		return
	}
	if event.Turn != c.turn || c.resetDone {
		return
	}
	switch event.Status {
	case "completed", "failed", "canceled":
		c.resetDone, c.success = true, event.Status == "completed"
		if !c.success {
			c.continueTask = false
		}
		if c.hostDone {
			c.phase, c.turn = compactCompleted, ""
		}
	}
}

func (c *Compaction) Completed(turn string, success bool) {
	if c.turn != turn || turn == "" {
		return
	}
	if c.requestID != "" && c.Active() {
		c.hostDone = true
		if !success {
			// Forced host cancellation is itself terminal evidence, including
			// a reset future aborted before it could publish its own receipt.
			c.continueTask = false
			if !c.resetDone {
				c.resetDone, c.success = true, false
			}
		}
		if !c.resetDone {
			return
		}
		c.phase, c.turn = compactCompleted, ""
		return
	}
	c.turn = ""
	if c.phase == compactRunning {
		c.phase, c.success = compactCompleted, success
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

// OwnsTurn also recognizes the first start notification before Started runs.
func (c *Compaction) OwnsTurn(turn string) bool {
	return turn != "" && (c.Starting() && c.requestID == "" || c.Active() && c.turn == turn)
}

func (c *Compaction) Pending() bool { return c.ackPending || c.Active() || c.turn != "" }
func (c *Compaction) AcknowledgementPending() bool {
	return c.ackPending
}
func (c *Compaction) ContinuationPending() bool { return c.continueTask }

// CancelQueued restores local intent without interrupting Main.
func (c *Compaction) CancelQueued(hasQueued bool) bool {
	if !hasQueued || c.Active() || c.AcknowledgementPending() {
		return false
	}
	c.cancelled, c.continueTask = true, false
	return true
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

// Continue consumes eligibility once. Waiting user input takes precedence over
// a native empty-input turn; idle or cancelled compaction stays idle.
func (c *Compaction) Continue(waitingInput bool) bool {
	if c.Pending() || !c.continueTask {
		return false
	}
	c.continueTask = false
	return !waitingInput
}
