package router

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// openActivityEdit resolves an exact host invocation, never a nearby edit or path.
func (u *terminalUI) openActivityEdit(view *liveActivityView, seq uint64, path string) bool {
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
			previous := u.navigationReturn()
			c.navigation.Changes.Query = ""
			c.filterCaller("")
			for _, node := range c.navigation.Changes.Nodes {
				if node.Change != attempt.change || len(node.Files) == 0 {
					continue
				}
				file := node.Files[0].File
				var focus *livediff.Chunk
				if path != "" {
					file = -1
					for _, candidate := range node.Files {
						for _, chunk := range c.view.Files[candidate.File].Chunks {
							if chunk.Change != attempt.change {
								continue
							}
							target := chunk.Review.AfterPath
							if target == "" {
								target = chunk.Review.BeforePath
							}
							if pathdisplay.ForWorkspace(cmp.Or(chunk.Workspace, c.workspace), target) == path {
								file = candidate.File
								focus = new(chunk)
								break
							}
						}
						if file >= 0 {
							break
						}
					}
					if file < 0 {
						continue
					}
				}
				u.navigationReturns = append(u.navigationReturns, previous)
				c.back = liveDiffBack{}
				u.side, u.diffOpen, u.activityOpen, u.focus = true, true, false, 1
				c.diffMode, c.dirty = true, true
				c.navigation.Hidden, c.navigation.Filtering = false, false
				c.navigation.ChangesTab, c.navigation.Focused = true, true
				c.openChange(node.Change, file)
				c.editPreview, c.editFocus = focus, focus
				c.navigation.Changes.FocusChange(node.Change, c.navRows)
				return true
			}
			u.restoreNavigationReturn(previous)
		}
	}
	return false
}
