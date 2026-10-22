package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (files *detailFiles) openDetailSelection(session *sessionpresentation.Attachment, version sessionpresentation.Version, source *sessionpresentation.ActivitySource, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage, processes bool) {
	u.Selection = nil
	files.stopFileRead()
	id := files.details.OpenDetails(version, source, pages, processes, session.RunningProcesses())
	u.Dialog = &terminalsession.ContentDialog{ID: id, View: view, Processes: processes}
	files.prepareOutput(session, u)
}

func (files *detailFiles) closeOutput(u *terminalsession.Attachment) {
	if u.Dialog != nil {
		files.details.CloseDetails(u.Dialog.ID)
	} else {
		files.details.Clear()
	}
	files.stopFileRead()
	u.Dialog = nil
}

func (files *detailFiles) replaceDetails(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment, pages []activitypresentation.DetailPage) {
	if terminal.Dialog == nil {
		return
	}
	files.details.RefreshDetails(terminal.Dialog.ID, pages, session.RunningProcesses())
	terminal.Dialog.Invalidate()
	files.prepareOutput(session, terminal)
}

// prepareOutput resolves the selected content before the renderer lays out.
// The terminal consumes one snapshot and owns no source or retained-output handle.
func (files *detailFiles) prepareOutput(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment) {
	d := terminal.Dialog
	if session.Scope() == nil {
		files.closeOutput(terminal)
		return
	}
	if d == nil || d.Selection != nil {
		return
	}
	revision, current := files.details.RefreshDetails(d.ID, nil, session.RunningProcesses())
	if !current {
		files.closeOutput(terminal)
		return
	}
	if d.Revision != revision {
		view, ok := files.details.DetailView(d.ID)
		if !ok {
			files.closeOutput(terminal)
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
	if output, ok := files.details.DetailOutput(request); ok {
		d.Output = &output
	}
}
