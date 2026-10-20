package router

import (
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

func (c *liveDiffTerminalController) scroll(key byte) bool {
	if !c.diffMode {
		return c.previewPane.Scroll(key)
	}
	if key == 'r' {
		return false
	}
	total := len(c.lines)
	if c.pinned {
		total++
	}
	next, _, ok := terminalui.PaneScroll(key, c.offset, c.rows, total)
	if !ok {
		return false
	}
	c.view.ScrollTo(c.rendering, next)
	c.offset = next
	c.dirty = true
	return true
}
