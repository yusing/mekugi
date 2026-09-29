package router

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
			var pages []activityui.Block
			selected := -1
			for _, chunk := range attempt.chunks {
				target := cmp.Or(chunk.Review.AfterPath, chunk.Review.BeforePath)
				display := pathdisplay.ForWorkspace(cmp.Or(chunk.Workspace, u.diff.workspace), target)
				if path == "" && selected < 0 || display == path {
					selected = len(pages)
				}
				pages = append(pages, activityui.Block{Kind: "op", Verb: "Edit", Path: display, Code: chunk.Review.UnifiedDiff(), Lang: "diff", Fenced: true})
			}
			if selected < 0 {
				continue
			}
			u.openBlocks(view, pages)
			u.output.showPage(selected)
			return true
		}
	}
	return false
}
