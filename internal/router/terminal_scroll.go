package router

import (
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

func (c *liveDiffTerminalController) scroll(key byte) bool {
	if !c.diffMode {
		return c.previewPane.Scroll(key)
	}
	total := len(c.lines)
	if c.pinned {
		total++
	}
	next, follow, ok := terminalui.PaneScroll(key, c.offset, c.rows, total)
	if !ok {
		return false
	}
	if follow {
		c.navigation.Focused, c.navigation.Filtering = false, false
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
