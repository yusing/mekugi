package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (u *appServerUI) holdCommand(entry sessionpresentation.ActivityEntry, frame activityui.CommandFrame) {
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.shell.Transcript, u.shell.Agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.HoldCommand(seq, frame)
		}
	}
}

func (u *appServerUI) releaseCommand(entry sessionpresentation.ActivityEntry) {
	at := u.now()
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.shell.Transcript, u.shell.Agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.ReleaseCommand(seq, at)
		}
	}
}
