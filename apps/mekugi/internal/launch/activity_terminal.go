package launch

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// deliverActivity transfers detached facts at an admitted transition. Neither
// terminal input nor painting has access to the reducer or retained outputs.
func deliverActivity(view *activityui.View, source *sessionpresentation.ActivitySource, session *sessionpresentation.Model, mainSource *sessionpresentation.ActivitySource, mainView *activityui.View) {
	if view == nil || source == nil {
		return
	}
	publication := source.Publication(session, mainSource)
	detached := false
	for i, entry := range publication.Entries {
		if view.NeedsCommandContent(entry.Seq, entry.Revision) {
			if !detached {
				publication = new(*publication)
				publication.Entries = slices.Clone(publication.Entries)
				detached = true
			}
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
	view.Update(publication, activityui.Display{MainCaller: display.Info.Caller, Caller: display.Displayed.Caller, SideCaller: display.SideThread.Caller, Attachment: display.Version.Attachment, Revision: display.Version.Revision})
}
