package router

import activityui "github.com/yusing/mekugi/internal/ui/activity"

// openErrors exposes the focused transcript's errors through the shared content
// dialog, newest first on opening. Left/Right reaches earlier retained errors.
func (u *terminalUI) openErrors() {
	var view *liveActivityView
	switch u.focus {
	case 0:
		if u.main != nil {
			view = u.main.view
		}
	case 2:
		view = u.agents
	}
	if view == nil {
		return
	}
	var pages []activityui.Block
	for _, entry := range view.entries {
		if entry.Kind != "error" || !view.visible(entry.activityPaneEntry) {
			continue
		}
		for _, block := range entry.blocks {
			block.Source = entry.Seq
			pages = append(pages, block)
		}
	}
	if len(pages) > 0 {
		u.openBlocks(view, pages)
		u.output.showPage(len(u.output.pages) - 1)
	}
}
