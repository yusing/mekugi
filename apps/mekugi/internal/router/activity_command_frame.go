package router

import (
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// commandFrame reprojects current admitted source and annotations with only its
// terminal outcome and output reveal delayed. Native handles stay in this adapter.
func commandFrame(record liveActivityRecord, frame activityui.CommandFrame) activitypresentation.Entry {
	content := activityContent(record.Source)
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

func (u *appServerUI) holdCommand(entry activityPaneEntry, output activityui.OutputFrame) {
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if seq := view.entrySeq(entry); seq != 0 {
			view.View.HoldCommand(seq, activityui.CommandFrame{Output: output})
		}
	}
}

func (u *appServerUI) holdTrackedCommand(entry *activityPaneEntry) {
	for _, view := range []*activityAdapter{u.view, u.agents} {
		for _, record := range view.Entries {
			if entry.native.sameItem(record.Source.native) {
				command := commandPresentation(record.Source)
				// Held segment targets continue resolving through their admitted
				// source until attribution settles; the neutral feed shows the
				// completed aggregate rather than these provisional segments.
				entry.native.segments, entry.native.segmentsPending = record.Source.native.segments, true
				view.View.HoldCommand(record.Source.Seq, activityui.CommandFrame{
					Output: activityui.OutputFrame{Tail: command.Tail, Omitted: command.TailOmitted}, Segments: command.Segments,
				})
				break
			}
		}
	}
}

func (u *appServerUI) releaseCommand(entry activityPaneEntry) {
	at := u.now()
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if seq := view.entrySeq(entry); seq != 0 {
			view.View.ReleaseCommand(seq, at)
		}
	}
}
