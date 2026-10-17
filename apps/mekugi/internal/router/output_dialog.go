package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func openOutput(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, activity *activitymodel.Service, u *terminalsession.Attachment, view *activityui.View, snippet activityui.Snippet) bool {
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
				if edits, _ := activityEditPages(attached, terminal, activity, view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	openDetails(attached, terminal, files, u, view, pages)
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
func openBlocks(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activityui.Block) {
	openDetails(attached, terminal, files, u, view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func openDetails(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	files.openDetailSelection(&attached.session, attached.presentation.View().Version, activitySource(attached, terminal, view), u, view, pages, false)
}

func openEntry(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, seq uint64) bool {
	for index, entry := range activitySource(attached, terminal, view).Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := activitySource(attached, terminal, view).SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		openDetails(attached, terminal, files, u, view, activitypresentation.DetailPages(blocks))
		return len(blocks) > 0
	}
	return false
}

func (main *appServerUI) outputIntent(u *terminalsession.Attachment, intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		main.detailFiles.closeOutput(&main.attached.session, u)
	case terminalsession.DialogCopy:
		copyText(&main.attached.feedback, main.shell, main.clock, u, intent.Text)
	case terminalsession.DialogReference:
		if !main.referenceInput(main.shell.Main.Reference("dialog", intent.Text, "")) {
			return
		}
		u.Focus = 0
		main.detailFiles.closeOutput(&main.attached.session, u)
	case terminalsession.DialogOpenLink:
		main.detailFiles.openMarkdownFile(&main.attached.session, main.attached.presentation, main.attached.cwd, activitySource(&main.attached, main.shell, u.Dialog.View), u, u.Dialog.View, intent.Text)
	}
}
