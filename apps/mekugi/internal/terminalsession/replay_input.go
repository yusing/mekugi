package terminalsession

import (
	"time"

	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

const (
	ReplayUp byte = 0x82 + iota
	ReplayDown
	ReplayRight
	ReplayLeft
	ReplayPageUp
	ReplayPageDown
	ReplayHome
	ReplayEnd
)

var replayNavigation = map[byte]string{
	ReplayUp: "\x1b[A", ReplayDown: "\x1b[B", ReplayRight: "\x1b[C", ReplayLeft: "\x1b[D",
	ReplayPageUp: "\x1b[5~", ReplayPageDown: "\x1b[6~", ReplayHome: "\x1b[H", ReplayEnd: "\x1b[F",
}

// ReplayInput decodes terminal reports before interpreting playback controls. State persists
// across reads, so fragmented mouse or navigation sequences cannot quit replay.
// Source: apps/mekugi/internal/router/session_ui_replay_input.go:25:101@70e7ce50 uiReplayInput
type ReplayInput struct {
	escape  string
	mouse   terminalui.Mouse
	x10     int
	timer   *time.Timer
	escapeC <-chan time.Time
}

func (in *ReplayInput) Close() {
	if in.timer != nil {
		in.timer.Stop()
	}
}

func (in *ReplayInput) Consume(key byte) byte {
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
		sequence := in.escape + string(key)
		in.escape = ""
		for action, report := range replayNavigation {
			if sequence == report {
				return action
			}
		}
	} else if len(in.escape) < 32 {
		in.escape += string(key)
	}
	return 0
}

// EscapeReady is armed only while a standalone Escape remains ambiguous.
func (in *ReplayInput) EscapeReady() <-chan time.Time { return in.escapeC }

// ExpireEscape acknowledges the timeout before the application dismisses a
// dialog or exits playback. It never creates an application effect itself.
func (in *ReplayInput) ExpireEscape() { in.escape, in.escapeC = "", nil }

// ReplayNavigation returns the ordinary pane key for a decoded navigation action.
func ReplayNavigation(key byte) string { return replayNavigation[key] }
