package router

import (
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

func (c *liveDiffTerminalController) scroll(key byte) bool {
	if key == 'r' {
		return false
	}
	total := len(c.lines) + 1
	next, _, ok := terminalui.PaneScroll(key, c.offset, c.rows, total)
	if !ok {
		return false
	}
	c.view.ScrollTo(c.rendering, next)
	c.offset = next
	c.dirty = true
	return true
}
