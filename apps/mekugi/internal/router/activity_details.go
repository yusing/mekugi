package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (u *terminalUI) openDetailSelection(view *activityui.View, pages []activitypresentation.DetailPage, processes bool) {
	u.selection = nil
	u.main.fileReads = nil
	id := u.main.session.OpenDetails(u.main.sessionPresentation().View().Version, u.main.activitySource(view), pages, processes)
	u.output = &terminalsession.ContentDialog{ID: id, View: view, Processes: processes}
	u.main.prepareOutput(u)
}

func (u *terminalUI) closeOutput() {
	if u.output != nil && u.main != nil {
		u.main.session.CloseDetails(u.output.ID)
		u.main.fileReads = nil
	}
	u.output = nil
}

func (u *appServerUI) replaceDetails(terminal *terminalUI, pages []activitypresentation.DetailPage) {
	if terminal.output == nil {
		return
	}
	u.session.RefreshDetails(terminal.output.ID, pages)
	terminal.output.Invalidate()
	u.prepareOutput(terminal)
}

// prepareOutput resolves the selected content before the renderer lays out.
// The terminal consumes one snapshot and owns no source or retained-output handle.
func (u *appServerUI) prepareOutput(terminal *terminalUI) {
	d := terminal.output
	if d == nil || d.Selection != nil {
		return
	}
	revision, current := u.session.RefreshDetails(d.ID, nil)
	if !current {
		terminal.closeOutput()
		return
	}
	if d.Revision != revision {
		view, ok := u.session.DetailView(d.ID)
		if !ok {
			terminal.closeOutput()
			return
		}
		d.Update(view)
	}
	if len(d.Pages) == 0 {
		return
	}
	page := d.Pages[d.Page]
	if !page.Retained {
		d.Output = nil
		return
	}
	if d.Output != nil && d.Output.Page == page.ID && d.Output.Version == page.OutputVersion {
		return
	}
	request := sessionpresentation.DetailOutputRequest{ID: d.ID, Page: page.ID, Version: page.OutputVersion}
	if output, ok := u.session.DetailOutput(request); ok {
		d.Output = &output
	}
}
