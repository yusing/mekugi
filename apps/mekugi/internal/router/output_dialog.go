package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func (u *terminalUI) openOutput(view *activityui.View, snippet activityui.Snippet) bool {
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
				if edits, _ := u.activityEditPages(view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	u.openDetails(view, pages)
	page := 0
	if block.Output != 0 && (len(block.Members) == 0 || block.Kind == "op") {
		for i, command := range u.output.Pages {
			if command.Source == block.Source && command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.output.ShowPage(page)
	return true
}

// openBlocks is the shared modal entry point. It never changes pane navigation.
func (u *terminalUI) openBlocks(view *activityui.View, pages []activityui.Block) {
	u.openDetails(view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func (u *terminalUI) openDetails(view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	u.openDetailSelection(view, pages, false)
}

func (u *terminalUI) openEntry(view *activityui.View, seq uint64) bool {
	for index, entry := range u.main.activitySource(view).Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := u.main.activitySource(view).SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		u.openDetails(view, activitypresentation.DetailPages(blocks))
		return len(blocks) > 0
	}
	return false
}

// openRequested opens an operation the view's pointer handling clicked.
func (u *terminalUI) openRequested(view *activityui.View) {
	if snippet := view.Opening; snippet != (activityui.Snippet{}) {
		view.Opening = activityui.Snippet{}
		if snippet.Block == activityui.EditNavigationSnippet {
			u.openActivityEdit(view, snippet.Run, snippet.Path)
		} else if !u.openOutput(view, snippet) {
			u.openEntry(view, snippet.Run)
		}
	}
}

func (u *terminalUI) outputKey(key string) { u.outputIntent(u.output.Key(key)) }
func (u *terminalUI) outputMouse(button, x, y int, release bool) {
	u.outputIntent(u.output.Mouse(button, x, y, release))
}

func (u *terminalUI) outputIntent(intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		u.closeOutput()
	case terminalsession.DialogCopy:
		u.copyText(intent.Text)
	case terminalsession.DialogReference:
		if u.main.composerReadOnly() {
			u.selectNativePane(0)
		}
		u.main.insertSelection("dialog", intent.Text, "")
		u.main.refreshPicker()
		u.main.Run = composer.NoRun
		u.Focus = 0
		u.closeOutput()
	case terminalsession.DialogOpenLink:
		u.main.openMarkdownFile(u, u.output.View, intent.Text)
	}
}
