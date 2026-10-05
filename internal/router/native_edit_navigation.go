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
	pages, path := u.activityEditPages(view, seq, path)
	return u.openEditPages(view, pages, path)
}

func (u *terminalUI) activityEditPages(view *liveActivityView, seq uint64, path string) ([]activityui.Block, string) {
	for _, entry := range view.entries {
		if entry.Seq != seq || entry.native == nil {
			continue
		}
		var order []string
		if u.diff != nil && u.diff.data != nil {
			order = u.diff.data.order
		}
		for _, key := range order {
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
			for _, chunk := range attempt.chunks {
				target := cmp.Or(chunk.Review.AfterPath, chunk.Review.BeforePath)
				workspace := cmp.Or(chunk.Workspace, u.diff.workspace)
				display := pathdisplay.ForWorkspace(workspace, target)
				if chunk.Review.Action().Title() == "Move" && path == pathdisplay.Move(workspace, chunk.Review.BeforePath, chunk.Review.AfterPath) {
					path = display
				}
				pages = append(pages, activityui.Block{Kind: "op", Verb: chunk.Review.Action().Title(), Path: display, Code: chunk.Review.UnifiedDiffForWorkspace(workspace), Lang: "diff", Fenced: true})
			}
			if slices.ContainsFunc(pages, func(page activityui.Block) bool { return path == "" || page.Path == path }) {
				return pages, path
			}
		}
		// Codex can finish a nested edit while sibling commands keep its cell
		// open. Its completed item already carries that invocation's diff;
		// opening it must not wait for the outer cell's durable capture.
		return entry.native.editPages, path
	}
	return nil, path
}

func (u *terminalUI) openEditPages(view *liveActivityView, pages []activityui.Block, path string) bool {
	for i, page := range pages {
		if path == "" || page.Path == path {
			u.openBlocks(view, pages)
			u.output.showPage(i)
			return true
		}
	}
	return false
}
