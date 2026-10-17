package router

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/contracts/observation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/commandsegments/segments"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const execTrackCompletionWait = 300 * time.Millisecond

// Terminal cursors reveal the existing invocation's admitted segment output.
type execTrackPresentation struct {
	segments []execSegmentOutput
	version  uint64
}

type execSegmentOutput struct {
	text   string
	output activityui.CommandOutput
}

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

// view reports the command's segments, and whether they changed since the
// last view. While live, a segment appears once it starts. Final views mark
// segments the list never reached as skipped; a segment still open when the
// shell finished is the one the shell exited in. The invocation owns facts and
// retained output; this adapter owns text projection and terminal reveal.
func (p *execTrackPresentation) view(invocation *activitypresentation.Invocation, track segments.Report, final bool, text func(string) string) (execTrackView, bool) {
	changed := p.version != track.Version
	p.version = track.Version
	for len(p.segments) < len(track.Segments) {
		p.segments = append(p.segments, execSegmentOutput{})
	}
	report := activitypresentation.SegmentReport{Terminal: track.Terminal, Lossy: track.Lossy, Ended: track.Ended, Closed: track.Closed, Done: track.Done, ExitCode: track.Code}
	for i, observed := range track.Segments {
		cursor := &p.segments[i]
		if cursor.text == "" {
			cursor.text = text(observed.Source)
			cursor.output = activityui.NewCommandOutput(instantOperations(cursor.text))
		}
		if !track.Lossy && observed.Output != "" {
			cursor.output.Write(observed.Output)
		}
		report.Segments = append(report.Segments, activitypresentation.SegmentObservation{
			Source: observed.Source, Text: cursor.text, Analysis: frontends.ShellDisplaySource(observed.Source),
			Started: observed.Timing.Started, Ended: observed.Timing.Ended, Duration: time.Duration(observed.Timing.ElapsedNS),
			Began: observed.Began, Finished: observed.Ended, ExitCode: observed.Code, Output: observed.Output,
		})
	}
	semantic, release := invocation.ObserveSegments(report)
	snapshot := invocation.ReportSnapshot(final)
	view := execTrackView{output: snapshot.SeparateOutput, ended: snapshot.Ended, complete: snapshot.Complete, code: snapshot.ExitCode, semantic: semantic, release: release}
	for index, segment := range snapshot.Segments {
		cursor := &p.segments[index]
		frame, advanced := cursor.output.Advance()
		changed = changed || advanced
		shown := sessionpresentation.ActivitySegment{Timing: segmenttransport.Timing{Started: segment.Started, Ended: segment.Ended, ElapsedNS: int64(segment.Duration)},
			Text: segment.Text, Source: segment.Source, Running: segment.Running, Skipped: segment.Skipped, Exit: segment.ExitCode, Raw: segment.Raw,
			Output: invocation.SegmentOutput(index),
		}
		if view.output {
			if final {
				frame = cursor.output.Settle()
			}
			shown.Tail, shown.Omit = frame.Tail, frame.Omitted
			if final && !shown.Skipped && shown.Exit == 0 {
				output := strings.Join(shown.Tail, "\n")
				shown.Changes = mchangesOutputRows(observation.Item{Command: shown.Source, AggregatedOutput: &output})
				if len(shown.Changes) > 0 && shown.Omit > 0 {
					shown.Changes = append([]activitypresentation.ChangeRow{{Note: activityui.Elision{Hidden: shown.Omit}.Text()}}, shown.Changes...)
				}
			}
		}
		view.segments = append(view.segments, shown)
	}
	return view, changed || semantic
}

// commandReportView drains the observer once before Activity reduction and
// terminal reveal. Attribution loss returns to the transport owner explicitly.
func (u *appServerUI) commandReportView(key [3]string, run *appServerCommandRun, final bool, directory string) (execTrackView, bool) {
	track, ok := u.execTrack.Read(key)
	if !ok {
		return execTrackView{}, false
	}
	view, changed := run.segments.view(run.entry.Native.Invocation, track, final, func(source string) string {
		return (activitypresentation.CommandSource{Script: source, Display: frontends.ShellDisplaySource(source), Directory: directory}).SegmentText()
	})
	if view.release {
		u.execTrack.ReleaseOutput(key)
	}
	return view, changed
}

// flushTrackedCommand re-sends a tracked command whose segments changed, and
// completes it once its report ends. It reports false for an untracked
// command, which keeps the host's single output stream.
func (u *appServerUI) flushTrackedCommand(key [3]string, run *appServerCommandRun) ([]sessionpresentation.ActivityEntry, bool) {
	s := &u.session
	if run.completion != nil {
		view, _ := u.commandReportView(key, run, false, run.entry.Native.CommandCwd)
		if !view.ended && u.now().Sub(run.completedAt) < execTrackCompletionWait {
			return nil, true
		}
		enriched, valid := u.reconcileTrackedCommand(key, *run.completion, run.completed)
		for _, adapter := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
			adapter.SettleCommandReport(enriched, valid)
		}
		u.releaseCommand(run.entry)
		delete(s.commands, key)
		u.execTrack.Forget(key)
		u.dirty = true
		return nil, true
	}
	view, changed := u.commandReportView(key, run, false, run.entry.Native.CommandCwd)
	if !u.execTrack.Tracking(key) {
		return nil, false
	}
	entry := run.entry
	native := *entry.Native
	native.Phase = "item/commandExecution/outputDelta"
	entry.Native, entry.Agent = &native, s.path(native.Thread)
	if view.ended && !view.complete {
		// The report ended early, as when the helper declined after the
		// match; the host's combined output applies from here on.
		if changed {
			run.output.Invalidate()
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
		frame, advanced = run.output.Advance()
		changed = changed || advanced
	}
	if !changed {
		return nil, true
	}
	native.Segments = view.segments
	u.holdCommand(entry, activityui.CommandFrame{Output: frame, Segments: sessionpresentation.CommandPresentation(entry).Segments})
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
	run := u.session.commands[key]
	view, _ := u.commandReportView(key, run, true, appServerCommandDirectory(item, u.session.cwd))
	if !run.entry.Native.Invocation.ReportMatchesHost() || len(view.segments) == 0 {
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
		entry.OutputTail, entry.OutputOmit = appServerOutputTail(item.AggregatedOutput)
		native.Changes, native.Commit = commandOutputRows(item)
	}
	return entry, true
}
