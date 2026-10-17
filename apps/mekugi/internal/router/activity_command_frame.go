package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (u *appServerUI) holdCommand(entry sessionpresentation.ActivityEntry, frame activityui.CommandFrame) {
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		if seq := source.EntrySeq(entry); seq != 0 {
			view.HoldCommand(seq, frame)
		}
	}
}

func (u *appServerUI) holdTrackedCommand(entry *sessionpresentation.ActivityEntry) {
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		for _, record := range source.Entries {
			if entry.Native.SameItem(record.Source.Native) {
				command := sessionpresentation.CommandPresentation(record.Source)
				// Held segment targets continue resolving through their admitted
				// source until attribution settles; the neutral feed shows the
				// completed aggregate rather than these provisional segments.
				entry.Native.Segments, entry.Native.SegmentsPending = record.Source.Native.Segments, true
				view.HoldCurrentCommand(record.Source.Seq, activityui.CommandFrame{
					Output: activityui.OutputFrame{Tail: command.Tail, Omitted: command.TailOmitted}, Segments: command.Segments,
				})
				break
			}
		}
	}
}

func (u *appServerUI) releaseCommand(entry sessionpresentation.ActivityEntry) {
	at := u.now()
	for i, source := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		view := []*activityui.View{u.view, u.agents}[i]
		if seq := source.EntrySeq(entry); seq != 0 {
			view.ReleaseCommand(seq, at)
		}
	}
}
