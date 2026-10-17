package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (files *detailFiles) openDetailSelection(session *sessionpresentation.Attachment, version sessionpresentation.Version, source *sessionpresentation.ActivitySource, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage, processes bool) {
	u.Selection = nil
	files.fileReads = nil
	id := session.OpenDetails(version, source, pages, processes)
	u.Dialog = &terminalsession.ContentDialog{ID: id, View: view, Processes: processes}
	files.prepareOutput(session, u)
}

func (files *detailFiles) closeOutput(session *sessionpresentation.Attachment, u *terminalsession.Attachment) {
	if u.Dialog != nil {
		session.CloseDetails(u.Dialog.ID)
		files.fileReads = nil
	}
	u.Dialog = nil
}

func (files *detailFiles) replaceDetails(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment, pages []activitypresentation.DetailPage) {
	if terminal.Dialog == nil {
		return
	}
	session.RefreshDetails(terminal.Dialog.ID, pages)
	terminal.Dialog.Invalidate()
	files.prepareOutput(session, terminal)
}

// prepareOutput resolves the selected content before the renderer lays out.
// The terminal consumes one snapshot and owns no source or retained-output handle.
func (files *detailFiles) prepareOutput(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment) {
	d := terminal.Dialog
	if d == nil || d.Selection != nil {
		return
	}
	revision, current := session.RefreshDetails(d.ID, nil)
	if !current {
		files.closeOutput(session, terminal)
		return
	}
	if d.Revision != revision {
		view, ok := session.DetailView(d.ID)
		if !ok {
			files.closeOutput(session, terminal)
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
	if output, ok := session.DetailOutput(request); ok {
		d.Output = &output
	}
}
