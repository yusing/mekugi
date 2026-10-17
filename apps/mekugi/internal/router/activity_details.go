package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (main *appServerUI) openDetailSelection(u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage, processes bool) {
	u.Selection = nil
	main.fileReads = nil
	id := main.session.OpenDetails(main.sessionPresentation().View().Version, main.activitySource(view), pages, processes)
	u.Dialog = &terminalsession.ContentDialog{ID: id, View: view, Processes: processes}
	main.prepareOutput(u)
}

func (main *appServerUI) closeOutput(u *terminalsession.Attachment) {
	if u.Dialog != nil && main != nil {
		main.session.CloseDetails(u.Dialog.ID)
		main.fileReads = nil
	}
	u.Dialog = nil
}

func (u *appServerUI) replaceDetails(terminal *terminalsession.Attachment, pages []activitypresentation.DetailPage) {
	if terminal.Dialog == nil {
		return
	}
	u.session.RefreshDetails(terminal.Dialog.ID, pages)
	terminal.Dialog.Invalidate()
	u.prepareOutput(terminal)
}

// prepareOutput resolves the selected content before the renderer lays out.
// The terminal consumes one snapshot and owns no source or retained-output handle.
func (u *appServerUI) prepareOutput(terminal *terminalsession.Attachment) {
	d := terminal.Dialog
	if d == nil || d.Selection != nil {
		return
	}
	revision, current := u.session.RefreshDetails(d.ID, nil)
	if !current {
		u.closeOutput(terminal)
		return
	}
	if d.Revision != revision {
		view, ok := u.session.DetailView(d.ID)
		if !ok {
			u.closeOutput(terminal)
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
