package router

import (
	"time"

	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

// Decode terminal reports before interpreting playback controls. State persists
// across reads, so fragmented mouse or navigation sequences cannot quit replay.
type uiReplayInput struct {
	escape  string
	mouse   terminalui.Mouse
	x10     int
	timer   *time.Timer
	escapeC <-chan time.Time
}

func (in *uiReplayInput) close() {
	if in.timer != nil {
		in.timer.Stop()
	}
}

func (in *uiReplayInput) consume(key byte) byte {
	if key == 3 {
		return key // Cancellation must still work after an incomplete report.
	}
	if key == 27 {
		in.escape, in.mouse, in.x10 = "\x1b", terminalui.Mouse{}, 0
		if in.timer == nil {
			in.timer = time.NewTimer(40 * time.Millisecond)
		} else {
			in.timer.Reset(40 * time.Millisecond)
		}
		in.escapeC = in.timer.C
		return 0
	}
	if in.x10 > 0 {
		in.x10-- // Legacy mouse reports contain three arbitrary bytes.
		return 0
	}
	if in.mouse.Active {
		action, _, _ := in.mouse.Consume(key)
		if action == 'j' || action == 'k' {
			return terminalui.PaneWheelKey(action)
		}
		return 0
	}
	if in.escape == "" {
		return key
	}
	in.timer.Stop()
	in.escapeC = nil
	if in.escape == "\x1b" {
		in.escape = ""
		if key == '[' || key == 'O' {
			in.escape = "\x1b" + string(key)
		}
		return 0
	}
	if in.escape == "\x1b[" {
		switch key {
		case '<':
			in.escape = ""
			in.mouse.Consume(key)
			return 0
		case 'M':
			in.escape, in.x10 = "", 3
			return 0
		}
	}
	if key >= 0x40 && key <= 0x7e {
		in.escape = ""
	}
	return 0
}
