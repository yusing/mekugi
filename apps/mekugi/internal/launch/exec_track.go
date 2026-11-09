package launch

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"

	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/contracts/observation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

const execTrackCompletionWait = 300 * time.Millisecond

// execTrackView is a report as presented at one frame.
type execTrackView struct {
	release  bool // Return attribution loss to the observation owner.
	segments []sessionpresentation.ActivitySegment
	semantic bool // Observed segment or attribution facts changed, rather than only reveal.
	output   bool // Segments carry their own output; otherwise the host's combined output applies.
	ended    bool // The report connection closed.
	complete bool // The report ended with the shell's own exit status.
	code     int
}

// commandReportView drains the observer once before Activity reduction and
// detached delivery. Attribution loss returns to the transport owner explicitly.
func commandReportView(attached *attachedPresentation, observer *commandsegments.Observer, key [3]string, run *sessionpresentation.CommandOperation, final bool, directory string) execTrackView {
	track, ok := observer.Read(key)
	if !ok {
		return execTrackView{}
	}
	report := sessionpresentation.CommandReport(run.Entry.Native.Invocation, track, directory)
	state := sessionpresentation.ObserveCommandReport(run.Entry.Native.Invocation, report, final)
	snapshot := state.Snapshot

	output := slices.ContainsFunc(report.Segments, func(segment activitypresentation.SegmentObservation) bool { return segment.Output != "" })
	if state.Changed || output || final {
		detached := snapshot
		detached.Segments = slices.Clone(snapshot.Segments)
		for i := range detached.Segments {
			detached.Segments[i].Raw = ""
			detached.Segments[i].Output = nil
		}
		attached.hooks.deliverActivity(attached.executionPresentation, []sessionpresentation.ActivityDelivery{{Report: &sessionpresentation.CommandReportDelivery{Key: key, Text: run.Entry.Text, MainSequence: attached.source.EntrySeq(run.Entry), ChildSequence: attached.childSource.EntrySeq(run.Entry), Report: report, Snapshot: detached, Version: track.Version, Final: final}}})
	}

	view := execTrackView{output: snapshot.SeparateOutput, ended: snapshot.Ended, complete: snapshot.Complete, code: snapshot.ExitCode, semantic: state.Changed, release: state.Release, segments: state.Segments}
	if view.release {
		observer.ReleaseOutput(key)
	}
	return view
}

// flushTrackedCommand re-sends a tracked command whose segments changed, and
// completes it once its report ends. Untracked commands keep the host's
// single output stream.
func flushTrackedCommand(attached *attachedPresentation, tracker *commandsegments.Observer, clock func() time.Time, parent context.Context, retained changes.Scope, segments *reports.Service, writes *chan commandSegmentWrite, pending *int, issues *requestexecution.Failures, key [3]string, run *sessionpresentation.CommandOperation) []sessionpresentation.ActivityEntry {
	s := &attached.session
	if run.Completion != nil {
		view := commandReportView(attached, tracker, key, run, false, run.Entry.Native.CommandCwd)
		if !view.ended && presentationTime(clock).Sub(run.CompletedAt) < execTrackCompletionWait {
			return nil
		}
		enriched, valid := reconcileTrackedCommand(attached, tracker, parent, retained, segments, writes, pending, issues, key, *run.Completion, run.Completed)
		for _, adapter := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
			adapter.SettleCommandReport(enriched, valid)
		}
		releaseCommand(attached, presentationTime(clock), run.Entry)
		delete(attached.session.Commands(), key)
		tracker.Forget(key)

		return nil
	}
	view := commandReportView(attached, tracker, key, run, false, run.Entry.Native.CommandCwd)
	if !tracker.Tracking(key) {
		return nil
	}
	entry := run.Entry
	native := *entry.Native
	native.Phase = "item/commandExecution/outputDelta"
	entry.Native, entry.Agent = &native, s.Caller(native.Thread)
	if view.ended && !view.complete {
		// The report ended early, as when the helper declined after the
		// match; the host's combined output applies from here on.
		if view.semantic {
			return []sessionpresentation.ActivityEntry{entry}
		}
		return nil
	}

	if !view.semantic {
		return nil
	}
	native.Segments = slices.Clone(view.segments)
	for i := range native.Segments {
		native.Segments[i].Tail, native.Segments[i].Omit, native.Segments[i].Clipped = nil, 0, false
	}
	return []sessionpresentation.ActivityEntry{entry}
}

// reconcileTrackedCommand admits segment outcomes when the report ended with
// the host's own exit status. Otherwise the report is incomplete, and the
// already-published host aggregate remains authoritative.
func reconcileTrackedCommand(attached *attachedPresentation, tracker *commandsegments.Observer, parent context.Context, retained changes.Scope, segments *reports.Service, writes *chan commandSegmentWrite, pending *int, issues *requestexecution.Failures, key [3]string, entry sessionpresentation.ActivityEntry, item observation.Item) (sessionpresentation.ActivityEntry, bool) {
	run := attached.session.Commands()[key]
	view := commandReportView(attached, tracker, key, run, true, sessionpresentation.CommandDirectory(item, attached.cwd))
	if !run.Entry.Native.Invocation.ReportMatchesHost() || len(view.segments) == 0 {
		return entry, false
	}
	native := *entry.Native
	native.Segments, native.SegmentsPending = view.segments, false
	retainCommandSegments(parent, retained, segments, attached, writes, pending, issues, entry, item, view)
	for i := range native.Segments {
		// A commit reads its object from the directory the host ran it in.
		if segment := &native.Segments[i]; !segment.Running && !segment.Pending && !segment.Skipped && segment.Exit == 0 && segment.Raw != "" && segment.Changes == nil {
			segment.Changes, segment.Commit = activitypresentation.VCSOutputRows(frontends.ShellDisplaySource(sessionpresentation.DisplayCommand(segment.Source)), vcsSegmentCwd(item.Cwd, native.Segments[:i]), segment.Raw)
		}
	}
	// Successful segments' output stays open until the agent's next event.
	entry.Completed = entry.Native.CommandEnded
	entry.Native = &native
	if !view.output {
		entry.OutputTail, entry.OutputOmit, entry.OutputClipped = sessionpresentation.OutputTail(item.AggregatedOutput)
		native.Changes, native.Commit = sessionpresentation.CommandOutputRows(item)
	}
	return entry, true
}
