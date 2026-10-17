package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// deliverActivity transfers detached facts at an admitted transition. Neither
// terminal input nor painting has access to the reducer or retained outputs.
func deliverActivity(view *activityui.View, source *sessionpresentation.ActivitySource, session *sessionpresentation.Model, mainSource *sessionpresentation.ActivitySource, mainView *activityui.View) {
	if view == nil || source == nil {
		return
	}
	publication := source.Publication(session, mainSource)
	for i, entry := range publication.Entries {
		if view.NeedsCommandContent(entry.Seq, entry.Revision) {
			publication.Entries[i].Command = new(sessionpresentation.ActivityContent(source.Entries[i].Source))
		}
	}
	if source.Audience != sessionpresentation.MainAudience {
		view.StatusLabel = source.Coverage
	}
	if mainView != nil {
		view.MainSummary = mainView.SummaryBlocks(publication.MainSummary)
		view.MainStatus = mainView.StatusLabel
	}
	display := session.View()
	view.Update(publication, activityui.Display{Caller: display.Displayed.Caller, Attachment: display.Version.Attachment, Revision: display.Version.Revision})
}

func (u *appServerUI) publishActivity() {
	refreshRosterRoles(&u.session, u.childSource)
	session := u.sessionPresentation()
	if u.shell.Transcript != nil {
		u.shell.Transcript.StatusLabel = textsanitize.Safe(terminalsession.StatusText(session.View().Status.State), false)
	}
	deliverActivity(u.shell.Transcript, u.source, session, nil, nil)
	deliverActivity(u.shell.Agents, u.childSource, session, u.source, u.shell.Transcript)
}

// activitySource resolves only a renderer already bound to this attachment.
func (u *appServerUI) activitySource(view *activityui.View) *sessionpresentation.ActivitySource {
	if view == u.shell.Transcript {
		return u.source
	}
	if view == u.shell.Agents {
		return u.childSource
	}
	return nil
}
