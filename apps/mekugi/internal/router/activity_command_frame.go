package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func holdCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, entry sessionpresentation.ActivityEntry, frame activityui.CommandFrame) {
	for i, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		view := []*activityui.View{terminal.Transcript, terminal.Agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.HoldCommand(seq, frame)
		}
	}
}

func releaseCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, at time.Time, entry sessionpresentation.ActivityEntry) {
	for i, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		view := []*activityui.View{terminal.Transcript, terminal.Agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.ReleaseCommand(seq, at)
		}
	}
}
