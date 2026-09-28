package router

import (
	"slices"
	"strings"
)

// openActivityEdit resolves an exact host invocation, never a nearby edit or path.
func (u *terminalUI) openActivityEdit(view *liveActivityView, seq uint64) bool {
	if u.diff == nil || u.diff.data == nil {
		return false
	}
	for _, entry := range view.entries {
		if entry.Seq != seq || entry.native == nil {
			continue
		}
		for _, key := range u.diff.data.order {
			attempt := u.diff.data.attempts[key]
			call, _, _ := strings.Cut(attempt.correlation, "\x00")
			match := attempt.thread == entry.native.thread && call == entry.native.item
			if receipt := attempt.receipt; receipt != nil {
				match = receipt.thread == entry.native.thread && slices.Contains(receipt.calls, entry.native.item)
			}
			if !match || len(attempt.chunks) == 0 {
				continue
			}
			c := u.diff
			c.navigation.Changes.Query = ""
			c.filterCaller("")
			for _, node := range c.navigation.Changes.Nodes {
				if node.Change != attempt.change || len(node.Files) == 0 {
					continue
				}
				u.pushNavigationReturn()
				c.back = liveDiffBack{}
				u.side, u.diffOpen, u.activityOpen, u.focus = true, true, false, 1
				c.diffMode, c.dirty = true, true
				c.navigation.Hidden, c.navigation.Filtering = false, false
				c.navigation.ChangesTab, c.navigation.Focused = true, true
				c.openChange(node.Change, node.Files[0].File)
				c.navigation.Changes.FocusChange(node.Change, c.navRows)
				return true
			}
		}
	}
	return false
}
