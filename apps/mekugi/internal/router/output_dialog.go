package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func (main *appServerUI) openOutput(u *terminalsession.Attachment, view *activityui.View, snippet activityui.Snippet) bool {
	block, ok := view.SnippetBlock(snippet)
	if !ok || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	pages := activitypresentation.DetailPages([]activitypresentation.Value{block.Value})
	if len(block.Members) > 0 {
		pages = nil
		seen := make(map[uint64]bool)
		for _, value := range block.Members {
			member := activityui.Block{Value: value}
			if member.EditSource != "" {
				if seen[member.Source] {
					continue
				}
				if edits, _ := main.activityEditPages(view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	main.openDetails(u, view, pages)
	page := 0
	if block.Output != 0 && (len(block.Members) == 0 || block.Kind == "op") {
		for i, command := range u.Dialog.Pages {
			if command.Source == block.Source && command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.Dialog.ShowPage(page)
	return true
}

// openBlocks is the shared modal entry point. It never changes pane navigation.
func (main *appServerUI) openBlocks(u *terminalsession.Attachment, view *activityui.View, pages []activityui.Block) {
	main.openDetails(u, view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func (main *appServerUI) openDetails(u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	main.detailFiles.openDetailSelection(&main.session, main.sessionPresentation().View().Version, main.activitySource(view), u, view, pages, false)
}

func (main *appServerUI) openEntry(u *terminalsession.Attachment, view *activityui.View, seq uint64) bool {
	for index, entry := range main.activitySource(view).Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := main.activitySource(view).SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		main.openDetails(u, view, activitypresentation.DetailPages(blocks))
		return len(blocks) > 0
	}
	return false
}

func (main *appServerUI) outputIntent(u *terminalsession.Attachment, intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		main.detailFiles.closeOutput(&main.session, u)
	case terminalsession.DialogCopy:
		main.copyText(u, intent.Text)
	case terminalsession.DialogReference:
		if !main.referenceInput(main.mainView.Reference("dialog", intent.Text, "")) {
			return
		}
		u.Focus = 0
		main.detailFiles.closeOutput(&main.session, u)
	case terminalsession.DialogOpenLink:
		main.detailFiles.openMarkdownFile(&main.session, main.sessionPresentation(), main.cwd, main.activitySource(u.Dialog.View), u, u.Dialog.View, intent.Text)
	}
}
