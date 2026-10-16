package router

import activityui "github.com/yusing/mekugi/modules/activity/terminalview"

// openErrors exposes the focused transcript's errors through the shared content
// dialog, newest first on opening. Left/Right reaches earlier retained errors.
func (u *terminalUI) openErrors() {
	var view *activityAdapter
	switch u.Focus {
	case 0:
		if u.main != nil {
			view = u.main.transcriptView()
		}
	}
	if view == nil {
		return
	}
	var pages []activityui.Block
	for _, entry := range view.Entries {
		if entry.Source.Kind != "error" || !view.Visible(entry.Publish()) {
			continue
		}
		for _, block := range view.Disclosure.Blocks(entry.Values) {
			block.Source = entry.Source.Seq
			pages = append(pages, block)
		}
	}
	if len(pages) > 0 {
		u.openBlocks(view, pages)
		u.output.ShowPage(len(u.output.pages) - 1)
	}
}
