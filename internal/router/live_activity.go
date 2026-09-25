package router

import (
	"github.com/yusing/mekugi/internal/livediff"
	"strings"
)

func (v *liveActivityView) handleKey(escape string, key byte) (string, bool) {
	if key == 3 {
		return "", true
	}
	// A background-color reply selects the syntax theme, as in the diff pane.
	// A byte that cannot occur in the reply ends the capture and is handled as
	// a key, so a stray Esc then ']' does not swallow later input.
	if v.osc.Active && !liveActivityOSCByte(key) {
		v.osc = livediff.OSC{}
	} else if v.osc.Active || escape == "\x1b" && key == ']' {
		if reply, complete := v.osc.Consume(key); complete {
			if theme, ok := livediff.BackgroundTheme(reply); ok {
				v.painter.theme = theme
			}
		}
		return "", false
	}
	if key == 27 {
		return "\x1b", false
	}
	if escape != "" {
		escape += string(key)
		switch escape {
		case "\x1b[", "\x1b[1", "\x1b[4", "\x1b[5", "\x1b[6", "\x1bO":
			return escape, false
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		case "\x1b[H", "\x1bOH", "\x1b[1~":
			key = 'g'
		case "\x1b[F", "\x1bOF", "\x1b[4~":
			key = 'G'
		case "\x1b[5~":
			key = 'b'
		case "\x1b[6~":
			key = ' '
		default:
			return "", false
		}
	}
	if v.scrollKey(key) {
		return "", false
	}
	switch key {
	case 'n', '\t':
		v.selectAgent(1)
	case 'p':
		v.selectAgent(-1)
	case 'o':
		v.only = !v.only
		v.follow()

	}
	return "", false
}

func liveActivityOSCByte(key byte) bool {
	switch {
	case key >= '0' && key <= '9', key >= 'a' && key <= 'f', key >= 'A' && key <= 'F':
		return true
	}
	return strings.IndexByte("rgb:/;?\\\a\x1b", key) >= 0
}
