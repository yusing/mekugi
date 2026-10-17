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
	u.refreshRosterRoles()
	session := u.sessionPresentation()
	if u.view != nil {
		u.view.StatusLabel = textsanitize.Safe(terminalsession.StatusText(session.View().Status.State), false)
	}
	deliverActivity(u.view, u.source, session, nil, nil)
	deliverActivity(u.agents, u.childSource, session, u.source, u.view)
}

// activitySource resolves only a renderer already bound to this attachment.
func (u *appServerUI) activitySource(view *activityui.View) *sessionpresentation.ActivitySource {
	if view == u.view {
		return u.source
	}
	if view == u.agents {
		return u.childSource
	}
	return nil
}

func renderActivityFeed(source *sessionpresentation.ActivitySource, view *activityui.View, width, rows int) activityui.Feed {
	for {
		feed := view.RenderFeed(width, rows)
		demands := view.OutputDemands()
		if len(demands) == 0 {
			return feed
		}
		view.Hydrate(source.ResolveOutputDemands(demands))
	}
}

func (u *appServerUI) takeDisplayIntent() *sessionpresentation.DisplayIntent {
	intent := u.agents.DisplayRequest
	if intent == nil {
		return nil
	}
	u.agents.DisplayRequest = nil
	return &sessionpresentation.DisplayIntent{
		Version: sessionpresentation.Version{Attachment: intent.Attachment, Revision: intent.Revision},
		Thread:  u.sessionPresentation().ActivityTarget(intent.Caller, u.childSource),
	}
}
