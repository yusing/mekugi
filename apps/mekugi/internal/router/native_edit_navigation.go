package router

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// openActivityEdit resolves an exact host invocation, never a nearby edit or path.
func (u *terminalUI) openActivityEdit(view *activityAdapter, seq uint64, path string) bool {
	pages, path := u.activityEditPages(view, seq, path)
	return u.openEditPages(view, pages, path)
}

func (u *terminalUI) activityEditPages(view *activityAdapter, seq uint64, path string) ([]activityui.Block, string) {
	for _, entry := range view.Entries {
		if entry.Source.Seq != seq || entry.Source.native == nil {
			continue
		}
		var order []string
		if u.diff != nil && u.saved.data != nil {
			order = u.saved.data.order
		}
		for _, key := range order {
			attempt := u.saved.data.attempts[key]
			call, _, _ := strings.Cut(attempt.correlation, "\x00")
			match := attempt.thread == entry.Source.native.thread && call == entry.Source.native.item
			if receipt := attempt.receipt; receipt != nil {
				match = receipt.thread == entry.Source.native.thread && slices.Contains(receipt.calls, entry.Source.native.item)
			}
			if !match || len(attempt.chunks) == 0 {
				continue
			}
			var pages []activityui.Block
			for _, chunk := range attempt.chunks {
				target := cmp.Or(chunk.Review.AfterPath, chunk.Review.BeforePath)
				workspace := cmp.Or(chunk.Workspace, u.diff.Workspace())
				display := pathdisplay.ForWorkspace(workspace, target)
				if chunk.Review.Action().Title() == "Move" && path == pathdisplay.Move(workspace, chunk.Review.BeforePath, chunk.Review.AfterPath) {
					path = display
				}
				pages = append(pages, activityui.Block{Kind: "op", Verb: chunk.Review.Action().Title(), Path: display, Code: chunk.Review.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(workspace, chunk.Review.BeforePath), pathdisplay.ForWorkspace(workspace, chunk.Review.AfterPath)), Lang: "diff", Fenced: true})
			}
			for _, block := range view.Disclosure.Blocks(entry.Values) {
				if block.DirectoryDeletion() && block.EditPath() == path {
					for _, page := range pages {
						if strings.HasPrefix(page.Path, strings.TrimSuffix(path, "/")+"/") {
							return pages, page.Path
						}
					}
				}
			}
			if slices.ContainsFunc(pages, func(page activityui.Block) bool { return path == "" || page.Path == path }) {
				return pages, path
			}
		}
		// Codex can finish a nested edit while sibling commands keep its cell
		// open. Its completed item already carries that invocation's diff;
		// opening it must not wait for the outer cell's durable capture.
		if receipt := entry.Source.native.capturedEdit; receipt != nil && len(receipt.pages) > 0 {
			return receipt.pages, path
		}
		return entry.Source.native.editPages, path
	}
	return nil, path
}

func (u *terminalUI) openEditPages(view *activityAdapter, pages []activityui.Block, path string) bool {
	for i, page := range pages {
		if path == "" || page.Path == path {
			u.openBlocks(view, pages)
			u.output.ShowPage(i)
			return true
		}
	}
	return false
}
