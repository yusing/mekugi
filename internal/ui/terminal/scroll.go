package terminal

const (
	PaneWheelUp    byte = 0x80
	PaneWheelDown  byte = 0x81
	paneWheelLines      = 3
)

func PaneWheelKey(action byte) byte {
	switch action {
	case 'j':
		return PaneWheelDown
	case 'k':
		return PaneWheelUp
	default:
		return action
	}
}

// Shared scroll offsets and default diff follow policy. Transcript consumers
// resume at the last full viewport and own their follow-key bindings.
func PaneScroll(key byte, offset, rows, total int) (next int, follow, handled bool) {
	next = offset
	switch key {
	case PaneWheelDown:
		next += paneWheelLines
	case PaneWheelUp:
		next -= paneWheelLines
	case 'j':
		next++
	case 'k':
		next--
	case ' ':
		next += max(1, rows)
	case 'b':
		next -= max(1, rows)
	case 'g':
		next = 0
	case 'G':
		next = max(0, total-rows)
	case 'r':
		return max(0, total-rows), true, true
	default:
		return offset, false, false
	}
	return min(max(0, next), max(0, total-1)), false, true
}
