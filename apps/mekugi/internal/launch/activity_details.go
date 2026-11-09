package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (files *detailFiles) openDetailSelection(session *sessionpresentation.Attachment, version sessionpresentation.Version, source *sessionpresentation.ActivitySource, pages []activitypresentation.DetailPage, processes bool) {
	files.stopFileRead()
	files.id = files.details.OpenDetails(version, source, pages, processes, session.RunningProcesses())
	files.agent = source != nil && source.Audience != sessionpresentation.MainAudience
	files.selected, files.file = "", nil
	if view, ok := files.details.DetailView(files.id); ok && len(view.Pages) > 0 {
		files.selected = view.Pages[0].ID
	}
}

func (files *detailFiles) closeOutput() {
	files.details.Clear()
	files.stopFileRead()
	files.id, files.selected, files.file = sessionpresentation.DetailID{}, "", nil
}

func (files *detailFiles) replaceDetails(session *sessionpresentation.Attachment, pages []activitypresentation.DetailPage) {
	files.details.RefreshDetails(files.id, pages, session.RunningProcesses())
}

func (files *detailFiles) detailView(session *sessionpresentation.Attachment) (sessionpresentation.DetailView, bool) {
	if session.Scope() == nil {
		files.closeOutput()
		return sessionpresentation.DetailView{}, false
	}
	if _, current := files.details.RefreshDetails(files.id, nil, session.RunningProcesses()); !current {
		files.closeOutput()
		return sessionpresentation.DetailView{}, false
	}
	return files.details.DetailView(files.id)
}

// Terminal adapters project the same semantic selection into a real renderer.
func (files *detailFiles) openTerminalDetailSelection(session *sessionpresentation.Attachment, version sessionpresentation.Version, source *sessionpresentation.ActivitySource, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage, processes bool) {
	u.Selection = nil
	files.openDetailSelection(session, version, source, pages, processes)
	u.Dialog = &terminalsession.ContentDialog{ID: files.id, View: view, Processes: processes}
	files.prepareOutput(session, u)
}

func (files *detailFiles) closeTerminalOutput(u *terminalsession.Attachment) {
	files.closeOutput()
	u.Dialog = nil
}

// prepareOutput resolves content before real terminal layout, preserving its
// frozen selection and locally selected page independently of the server.
func (files *detailFiles) prepareOutput(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment) {
	if files.id.Selection == 0 || files.pendingFile {
		terminal.Dialog = nil
		return
	}
	d := terminal.Dialog
	if d == nil || d.ID != files.id {
		view := terminal.Transcript
		if files.agent {
			view = terminal.Agents
		}
		d = &terminalsession.ContentDialog{ID: files.id, View: view}
		terminal.Dialog = d
	}
	d.File = files.file
	if d.Selection != nil {
		return
	}
	view, current := files.detailView(session)
	if !current {
		terminal.Dialog = nil
		return
	}
	if d.Revision != view.Revision {
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
