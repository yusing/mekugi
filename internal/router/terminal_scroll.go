package router

const (
	paneWheelUp    byte = 0x80
	paneWheelDown  byte = 0x81
	paneWheelLines      = 3
)

func paneWheelKey(action byte) byte {
	switch action {
	case 'j':
		return paneWheelDown
	case 'k':
		return paneWheelUp
	default:
		return action
	}
}

// All scrollable panes use the same line/page/home/end/follow semantics.
// End goes to the bottom but stays paused; only r resumes automatic following.
func paneScroll(key byte, offset, rows, total int) (next int, follow, handled bool) {
	next = offset
	switch key {
	case paneWheelDown:
		next += paneWheelLines
	case paneWheelUp:
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

func (c *liveDiffTerminalController) scroll(key byte) bool {
	if !c.diffMode {
		return c.previewPane.scroll(key)
	}
	total := len(c.lines)
	if c.pinned {
		total++
	}
	next, follow, ok := paneScroll(key, c.offset, c.rows, total)
	if !ok {
		return false
	}
	if follow {
		c.navigation.focused, c.navigation.filtering = false, false
		c.view.FollowLatest()
		c.followDirty = true
	} else {
		c.view.Following = false
		c.view.ScrollTo(c.rendering, next)
		c.offset = next
	}
	c.dirty = true
	return true
}
