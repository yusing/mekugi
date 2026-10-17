package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// openErrorDetails exposes the selected transcript's errors through the shared content
// dialog, newest first on opening. Left/Right reaches earlier retained errors.
func (main *appServerUI) openErrorDetails(u *terminalsession.Attachment, view *activityui.View) {
	if view == nil {
		return
	}
	var pages []activityui.Block
	for _, entry := range activitySource(&main.attached, main.shell, view).Entries {
		if entry.Source.Kind != "error" || !view.Visible(entry.Publish()) {
			continue
		}
		for _, block := range view.Disclosure.Blocks(entry.Values) {
			block.Source = entry.Source.Seq
			pages = append(pages, block)
		}
	}
	if len(pages) > 0 {
		openBlocks(&main.attached, main.shell, &main.detailFiles, u, view, pages)
		u.Dialog.ShowPage(len(u.Dialog.Pages) - 1)
	}
}
