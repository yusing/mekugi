package router

import (
	"strconv"
	"strings"
)

// SGR mouse reports can span reads. Consume the whole report, including ignored
// clicks and malformed payloads, so its bytes never become review commands.
type liveDiffMouse struct {
	active, discard bool
	payload         string
}

func (m *liveDiffMouse) consume(key byte) (action byte, row int) {
	if !m.active {
		m.active = true // The caller consumed CSI and passed its '<' introducer.
		return 0, 0
	}
	if key != 'M' && key != 'm' {
		if len(m.payload) >= 48 || key != ';' && (key < '0' || key > '9') {
			m.discard = true
		}
		if !m.discard {
			m.payload += string(key)
		}
		return 0, 0
	}
	payload, discard := m.payload, m.discard
	*m = liveDiffMouse{}
	if discard || key == 'm' { // Button releases do not scroll.
		return 0, 0
	}
	fields := strings.Split(payload, ";")
	if len(fields) != 3 {
		return 0, 0
	}
	var values [3]int
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 || i > 0 && value == 0 {
			return 0, 0
		}
		values[i] = value
	}
	// Ignore Shift/Alt/Ctrl modifiers, but not motion or unknown button bits.
	switch values[0] &^ 28 {
	case 64:
		return 'k', values[2]
	case 65:
		return 'j', values[2]
	case 66:
		return 'h', values[2]
	case 67:
		return 'l', values[2]
	}
	return 0, 0
}
