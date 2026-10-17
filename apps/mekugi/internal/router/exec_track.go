package router

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/contracts/observation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

const execTrackCompletionWait = 300 * time.Millisecond

// execTrackView is a report as presented at one frame.
type execTrackView struct {
	release  bool // Return attribution loss to the observation owner.
	segments []sessionpresentation.ActivitySegment
	frames   []activitypresentation.CommandSegment
	semantic bool // Observed segment or attribution facts changed, rather than only reveal.
	output   bool // Segments carry their own output; otherwise the host's combined output applies.
	ended    bool // The report connection closed.
	complete bool // The report ended with the shell's own exit status.
	code     int
}

// commandReportView drains the observer once before Activity reduction and
// terminal reveal. Attribution loss returns to the transport owner explicitly.
func (u *appServerUI) commandReportView(key [3]string, run *sessionpresentation.CommandOperation, final bool, directory string) (execTrackView, bool) {
	track, ok := u.execTrack.Read(key)
	if !ok {
		return execTrackView{}, false
	}
	report := sessionpresentation.CommandReport(run.Entry.Native.Invocation, track, directory)
	stream := u.streams.Command(key, run.Entry.Text)
	state := sessionpresentation.ObserveCommandReport(run.Entry.Native.Invocation, report, final)
	snapshot := state.Snapshot
	frames, changed := stream.RevealSegments(report, snapshot, track.Version, final)
	view := execTrackView{output: snapshot.SeparateOutput, ended: snapshot.Ended, complete: snapshot.Complete, code: snapshot.ExitCode, semantic: state.Changed, release: state.Release, segments: state.Segments}
	for index, segment := range snapshot.Segments {
		shown := segment.CommandSegment
		if view.output {
			shown.Tail, shown.TailOmitted = frames[index].Tail, frames[index].Omitted
		}
		shown.Changes = state.Segments[index].Changes
		view.frames = append(view.frames, shown)
	}
	if view.release {
		u.execTrack.ReleaseOutput(key)
	}
	return view, changed || state.Changed
}

// flushTrackedCommand re-sends a tracked command whose segments changed, and
// completes it once its report ends. It reports false for an untracked
// command, which keeps the host's single output stream.
func (u *appServerUI) flushTrackedCommand(key [3]string, run *sessionpresentation.CommandOperation) ([]sessionpresentation.ActivityEntry, bool) {
	s := &u.attached.session
	if run.Completion != nil {
		view, _ := u.commandReportView(key, run, false, run.Entry.Native.CommandCwd)
		if !view.ended && u.now().Sub(run.CompletedAt) < execTrackCompletionWait {
			return nil, true
		}
		enriched, valid := u.reconcileTrackedCommand(key, *run.Completion, run.Completed)
		for _, adapter := range []*sessionpresentation.ActivitySource{u.attached.source, u.attached.childSource} {
			adapter.SettleCommandReport(enriched, valid)
		}
		u.releaseCommand(run.Entry)
		delete(u.attached.session.Commands(), key)
		delete(u.streams.Commands, key)
		u.execTrack.Forget(key)
		u.dirty = true
		return nil, true
	}
	view, changed := u.commandReportView(key, run, false, run.Entry.Native.CommandCwd)
	if !u.execTrack.Tracking(key) {
		return nil, false
	}
	entry := run.Entry
	native := *entry.Native
	native.Phase = "item/commandExecution/outputDelta"
	entry.Native, entry.Agent = &native, s.Caller(native.Thread)
	if view.ended && !view.complete {
		// The report ended early, as when the helper declined after the
		// match; the host's combined output applies from here on.
		if changed {
			u.streams.Commands[key].Output.Invalidate()
		}
		if view.semantic {
			return []sessionpresentation.ActivityEntry{entry}, false
		}
		return nil, false
	}
	// Terminal and over-bound commands keep the host's combined output.
	var frame activityui.OutputFrame
	if !view.output {
		var advanced bool
		frame, advanced = u.streams.Commands[key].Output.Advance()
		changed = changed || advanced
	}
	if !changed {
		return nil, true
	}
	native.Segments = view.segments
	u.holdCommand(entry, activityui.CommandFrame{Output: frame, Segments: view.frames})
	u.dirty = true
	if !view.semantic {
		return nil, true
	}
	native.Segments = slices.Clone(view.segments)
	for i := range native.Segments {
		native.Segments[i].Tail, native.Segments[i].Omit = nil, 0
	}
	return []sessionpresentation.ActivityEntry{entry}, true
}

// reconcileTrackedCommand admits segment outcomes when the report ended with
// the host's own exit status. Otherwise the report is incomplete, and the
// already-published host aggregate remains authoritative.
func (u *appServerUI) reconcileTrackedCommand(key [3]string, entry sessionpresentation.ActivityEntry, item observation.Item) (sessionpresentation.ActivityEntry, bool) {
	run := u.attached.session.Commands()[key]
	view, _ := u.commandReportView(key, run, true, sessionpresentation.CommandDirectory(item, u.attached.cwd))
	if !run.Entry.Native.Invocation.ReportMatchesHost() || len(view.segments) == 0 {
		return entry, false
	}
	native := *entry.Native
	native.Segments, native.SegmentsPending = view.segments, false
	u.retainCommandSegments(entry, item, view)
	for i := range native.Segments {
		// A commit reads its object from the directory the host ran it in.
		if segment := &native.Segments[i]; segment.Raw != "" && segment.Changes == nil {
			segment.Changes, segment.Commit = activitypresentation.VCSOutputRows(frontends.ShellDisplaySource(sessionpresentation.DisplayCommand(segment.Source)), vcsSegmentCwd(item.Cwd, native.Segments[:i]), segment.Raw)
		}
	}
	// Successful segments' output stays open until the agent's next event.
	entry.Completed = entry.Native.CommandEnded
	entry.Native = &native
	if !view.output {
		entry.OutputTail, entry.OutputOmit = sessionpresentation.OutputTail(item.AggregatedOutput)
		native.Changes, native.Commit = sessionpresentation.CommandOutputRows(item)
	}
	return entry, true
}
