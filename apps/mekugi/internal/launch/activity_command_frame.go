package launch

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
	if attached.deliverActivity != nil && entry.Native != nil {
		attached.deliverActivity(attached.executionPresentation, []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Kind: sessionpresentation.CommandRelease, Key: [3]string{entry.Native.Thread, entry.Native.Turn, entry.Native.Item}, MainSequence: attached.source.EntrySeq(entry), ChildSequence: attached.childSource.EntrySeq(entry), At: at}}})
	}
	for i, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		view := []*activityui.View{terminal.Transcript, terminal.Agents}[i]
		if seq := source.EntrySeq(entry); view != nil && seq != 0 {
			view.ReleaseCommand(seq, at)
		}
	}
}
