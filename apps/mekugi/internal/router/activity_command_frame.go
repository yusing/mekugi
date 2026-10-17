package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// commandFrame reprojects current admitted source and annotations with only its
// terminal outcome and output reveal delayed. Native handles stay in this adapter.
func commandFrame(record sessionpresentation.ActivityRecord, frame activityui.CommandFrame) activitypresentation.Entry {
	content := sessionpresentation.ActivityContent(record.Source)
	content.Command.Running, content.Command.Exited, content.Command.ExitCode = true, false, 0
	content.Command.Ended, content.Command.Duration = time.Time{}, 0
	content.Command.Tail, content.Command.TailOmitted = frame.Output.Tail, frame.Output.Omitted
	content.Command.Segments = frame.Segments
	content.Details.Status = "inProgress"
	values := activitypresentation.RetainAnnotations(content.Values(), record.Values)
	facts := record.Source.ActivityFacts()
	facts.Running, facts.ItemComplete, facts.Unconfirmed, facts.OutputComplete = true, false, false, false
	facts.Completed = time.Time{}
	return activitypresentation.Entry{Facts: facts, Revision: record.Revision, Values: values}
}

func (u *appServerUI) holdCommand(entry sessionpresentation.ActivityEntry, frame activityui.CommandFrame) {
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if seq := view.EntrySeq(entry); seq != 0 {
			view.View.HoldCommand(seq, frame)
		}
	}
}

func (u *appServerUI) holdTrackedCommand(entry *sessionpresentation.ActivityEntry) {
	for _, view := range []*activityAdapter{u.view, u.agents} {
		for _, record := range view.Entries {
			if entry.Native.SameItem(record.Source.Native) {
				command := sessionpresentation.CommandPresentation(record.Source)
				// Held segment targets continue resolving through their admitted
				// source until attribution settles; the neutral feed shows the
				// completed aggregate rather than these provisional segments.
				entry.Native.Segments, entry.Native.SegmentsPending = record.Source.Native.Segments, true
				view.View.HoldCurrentCommand(record.Source.Seq, activityui.CommandFrame{
					Output: activityui.OutputFrame{Tail: command.Tail, Omitted: command.TailOmitted}, Segments: command.Segments,
				})
				break
			}
		}
	}
}

func (u *appServerUI) releaseCommand(entry sessionpresentation.ActivityEntry) {
	at := u.now()
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if seq := view.EntrySeq(entry); seq != 0 {
			view.View.ReleaseCommand(seq, at)
		}
	}
}
