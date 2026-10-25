// Package terminalsession owns the attached terminal's input and presentation state.
package terminalsession

import (
	"strconv"
	"strings"
	"time"
)

type InputKind uint8

const (
	NoInput InputKind = iota
	KeyInput
	PasteInput
	PasteStart
	PasteEnd
	ColorInput
	FocusInput
	MouseInput
)

type Input struct {
	Kind InputKind
	Text string
}

// Decoder keeps terminal replies and opaque paste bytes out of key bindings.
// Source: apps/mekugi/internal/router/terminal_ui.go:75:188@70e7ce50 terminalUI.key
type Decoder struct {
	paste                bool
	pasteEnd             int
	reply                []byte
	discard, replyEscape bool
	sequence             string
	escapeAt             time.Time
}

func (d *Decoder) Pasting() bool { return d.paste }
func (d *Decoder) Pending() bool { return d.sequence != "" }

func (d *Decoder) Feed(key byte, now time.Time) Input {
	if d.paste {
		const end = "\x1b[201~"
		if key == end[d.pasteEnd] {
			d.pasteEnd++
			if d.pasteEnd == len(end) {
				d.paste, d.pasteEnd = false, 0
				return Input{Kind: PasteEnd}
			}
			return Input{}
		}
		// A partial terminator is payload unless the entire marker matches.
		// Keep a new Escape as the next candidate without losing the old one.
		payload := end[:d.pasteEnd]
		d.pasteEnd = 0
		if key == 27 {
			d.pasteEnd = 1
		} else {
			payload += string([]byte{key})
		}
		if payload == "" {
			return Input{}
		}
		return Input{PasteInput, payload}
	}
	if d.reply != nil {
		// Cancellation and a new Escape recover an interrupted reply. Keep
		// Escape pending until the next byte distinguishes ST from new input.
		if key == 3 || key == 24 || key == 26 || d.replyEscape && key != '\\' && key != 7 {
			escaped := d.replyEscape
			d.reply, d.discard, d.replyEscape = nil, false, false
			if escaped && key != 27 && key != 3 && key != 24 && key != 26 {
				d.sequence = "\x1b"
			}
			return d.Feed(key, now)
		}
		done := key == 7 || d.replyEscape && key == '\\'
		d.replyEscape = key == 27
		if len(d.reply) < 4<<20 && !d.discard {
			d.reply = append(d.reply, key)
		} else {
			d.discard = true
		}
		if !done {
			return Input{}
		}
		var input Input
		if !d.discard {
			input = Input{ColorInput, string(d.reply)}
		}
		d.reply, d.discard, d.replyEscape = nil, false, false
		return input
	}
	if d.sequence == "\x1b" && key == ']' {
		d.sequence, d.reply = "", []byte{27, ']'}
		return Input{}
	}
	if d.sequence != "" {
		d.sequence += string(key)
		if len(d.sequence) > 128 {
			d.sequence = ""
			return Input{}
		}
		s := d.sequence
		if s == "\x1b[" || s == "\x1bO" || len(s) > 2 && s[1] == '[' && (key < 0x40 || key > 0x7e) {
			return Input{}
		}
		d.sequence = ""
		switch s {
		case "\x1b[I", "\x1b[O":
			return Input{FocusInput, s}
		case "\x1b[200~":
			d.paste, d.pasteEnd = true, 0
			return Input{Kind: PasteStart}
		case "\x1b[201~":
			return Input{Kind: PasteEnd}
		}
		if strings.HasPrefix(s, "\x1b[<") {
			return Input{MouseInput, s}
		}
		return Input{KeyInput, s}
	}
	if key == 27 {
		d.sequence, d.escapeAt = "\x1b", now
		return Input{}
	}
	return Input{KeyInput, string([]byte{key})}
}

// Escape completes a lone Escape only after its sequence deadline.
func (d *Decoder) Escape(now time.Time) bool {
	if d.sequence != "\x1b" || now.Sub(d.escapeAt) < 40*time.Millisecond {
		return false
	}
	d.sequence = ""
	return true
}

type Mouse struct {
	Button, X, Y int
	Release      bool
}

// ParseMouse decodes the terminal's SGR pointer report into zero-based cells.
func ParseMouse(s string) (Mouse, bool) {
	if len(s) < 4 || !strings.HasPrefix(s, "\x1b[<") {
		return Mouse{}, false
	}
	fields := strings.Split(s[3:len(s)-1], ";")
	if len(fields) != 3 {
		return Mouse{}, false
	}
	var v [3]int
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return Mouse{}, false
		}
		v[i] = n
	}
	return Mouse{v[0], v[1] - 1, v[2] - 1, s[len(s)-1] == 'm'}, true
}
