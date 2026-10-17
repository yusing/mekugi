package router

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"mvdan.cc/sh/v3/syntax"

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

// execPreviewTrack interprets matched reports within the application's admitted
// writer scope. The subscription itself neither captures nor publishes files.
type execPreviewTrack struct {
	*segments.Subscription
	writers    map[string]bool
	streamable map[string]bool
}

func newExecPreviewTrack(observer *segments.Observer, thread, turn string, commands []commandanalysis.Command) *execPreviewTrack {
	scripts := make([]string, len(commands))
	for i, command := range commands {
		scripts[i] = command.Command
	}
	subscription := observer.Subscribe(thread, turn, scripts)
	if subscription == nil {
		return nil
	}
	p := &execPreviewTrack{Subscription: subscription, writers: make(map[string]bool)}
	for _, command := range commands {
		plan := execscope.Classify(execscope.Input{Command: command.Command, Directory: command.Workdir, Shell: command.Shell})
		p.writers[p.Source(command.Command, "")] = plan.Class != execscope.Neutral && (len(plan.Programs) == 0 || slices.ContainsFunc(plan.Programs, func(program execscope.Program) bool { return program.Direct }))
	}
	return p
}

func execSegmentEdits(source string) bool {
	// Activity retains write classification for formatters and file copies.
	// Live previews use the narrower admitted source-segment scope.
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "")
	writes := false
	if err == nil {
		syntax.Walk(program, func(node syntax.Node) bool {
			if _, declaration := node.(*syntax.FuncDecl); declaration {
				return false
			}
			if call, ok := node.(*syntax.CallExpr); ok {
				args := call.Args
				for len(args) > 0 {
					name, literal := shellsyntax.Literal(args[0])
					if !literal || filepath.Base(name) != "env" {
						break
					}
					var reason string
					args, reason = shellsyntax.EnvArgs(args[1:])
					if reason != "" {
						break
					}
				}
				if words, literal := shellsyntax.Args(args); literal && len(words) > 0 {
					name := filepath.Base(words[0])
					writes = writes || name == "cp" || name == "install" || execscope.GoFormatterWrites(name, words[1:])
				}
			}
			return !writes
		})
	}
	if writes {
		return true
	}
	for _, block := range activitypresentation.Operations(execSegmentText(source)) {
		if slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
			return true
		}
	}
	return false
}

type execPreviewState struct {
	tracked, settled bool
	completed        int
	calls            []string
	changed          <-chan struct{}
}

func (p *execPreviewTrack) state() (state execPreviewState) {
	if p == nil {
		return state
	}
	observed := p.State()
	state.changed, state.settled = observed.Changed, true
	allTracked, writing := true, false
	for _, retained := range observed.Matches {
		script := retained.Source
		if retained.Ambiguous {
			return execPreviewState{tracked: true, changed: observed.Changed}
		}
		match := retained.Report
		streamable := p.streamable == nil || p.streamable[script]
		if match != nil && retained.HostCompleted {
			state.completed++
			for _, segment := range match.Segments {
				if segment.Ended {
					state.completed++
				}
			}
			state.calls = append(state.calls, retained.Key[2])
			state.tracked = state.tracked || streamable
			continue
		}
		if match == nil || match.HostOnly || match.Ended && !match.Done {
			allTracked = allTracked && !streamable
			writing = writing || match != nil && p.writers[script]
			continue
		}
		state.tracked = state.tracked || streamable
		last, completed := match.LastStarted(), 0
		for i, segment := range match.Segments {
			if segment.Ended {
				completed++
			}
			writer := false
			if p.streamable != nil {
				writer = p.streamable[p.Source(segment.Source, "")]
			} else {
				writer = execSegmentEdits(segment.Source)
			}
			if streamable && writer && !segment.Ended && !match.Done && (segment.Began || i > last) {
				state.settled = false
			}
		}
		// A receipt covers observed endpoints of this invocation. A later
		// command in the same shell does not hold its earlier edits hostage.
		// A sibling with no completed boundary cannot lend its writes to it.
		writing = writing || completed == 0 && last >= 0 && p.writers[script] && !match.Done
		if completed > 0 {
			state.completed += completed
			state.calls = append(state.calls, retained.Key[2])
		}
	}
	state.tracked = state.tracked && allTracked
	state.settled = state.tracked && state.settled
	if writing {
		state.completed, state.calls = 0, nil
	}
	return state
}

// commandSegment is one segment as the Activity shows it.
type commandSegment struct {
	timing  segmenttransport.Timing
	text    string // Display operations for the segment's source.
	running bool
	skipped bool // The list short-circuited before it.
	exit    int
	tail    []string
	omit    int
	changes []activitypresentation.ChangeRow
	output  *activitymodel.Output
	source  string
	raw     string                         // A finished VCS segment's complete output, for its change rows.
	commit  activitypresentation.CommitKey // The commit object whose files complete changes.
}

// execTrackView is a report as presented at one frame.
type execTrackView struct {
	release  bool // Return attribution loss to the observation owner.
	segments []commandSegment
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
			Source: observed.Source, Text: cursor.text, Analysis: toolActivityShellSource(observed.Source),
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
		shown := commandSegment{timing: segmenttransport.Timing{Started: segment.Started, Ended: segment.Ended, ElapsedNS: int64(segment.Duration)},
			text: segment.Text, source: segment.Source, running: segment.Running, skipped: segment.Skipped, exit: segment.ExitCode, raw: segment.Raw,
			output: invocation.SegmentOutput(index),
		}
		if view.output {
			if final {
				frame = cursor.output.Settle()
			}
			shown.tail, shown.omit = frame.Tail, frame.Omitted
			if final && !shown.skipped && shown.exit == 0 {
				output := strings.Join(shown.tail, "\n")
				shown.changes = mchangesOutputRows(observation.Item{Command: shown.source, AggregatedOutput: &output})
				if len(shown.changes) > 0 && shown.omit > 0 {
					shown.changes = append([]activitypresentation.ChangeRow{{Note: activityui.Elision{Hidden: shown.omit}.Text()}}, shown.changes...)
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
	view, changed := run.segments.view(run.entry.native.invocation, track, final, func(source string) string {
		return execSegmentTextInDirectory(source, directory)
	})
	if view.release {
		u.execTrack.ReleaseOutput(key)
	}
	return view, changed
}

// execSegmentText is a segment's display operations, using the same shell
// classifier as an untracked command.
func execSegmentText(source string) string {
	return execSegmentTextInDirectory(source, "")
}

func execSegmentTextInDirectory(source, cwd string) string {
	if text := activitypresentation.ShellSource(toolActivityShellSource(source), cwd); strings.TrimSpace(text) != "" {
		return text
	}
	if strings.ContainsAny(source, "\r\n") {
		source = toolActivityShellSource(source)
	}
	return activitypresentation.RunSource(source)
}

// flushTrackedCommand re-sends a tracked command whose segments changed, and
// completes it once its report ends. It reports false for an untracked
// command, which keeps the host's single output stream.
func (u *appServerUI) flushTrackedCommand(key [3]string, run *appServerCommandRun) ([]activityPaneEntry, bool) {
	s := &u.session
	if run.completion != nil {
		view, _ := u.commandReportView(key, run, false, run.entry.native.commandCwd)
		if !view.ended && u.now().Sub(run.completedAt) < execTrackCompletionWait {
			return nil, true
		}
		enriched, valid := u.reconcileTrackedCommand(key, *run.completion, run.completed)
		for _, adapter := range []*activityAdapter{u.view, u.agents} {
			adapter.settleCommandReport(enriched, valid)
		}
		u.releaseCommand(run.entry)
		delete(s.commands, key)
		u.execTrack.Forget(key)
		u.dirty = true
		return nil, true
	}
	view, changed := u.commandReportView(key, run, false, run.entry.native.commandCwd)
	if !u.execTrack.Tracking(key) {
		return nil, false
	}
	entry := run.entry
	native := *entry.native
	native.phase = "item/commandExecution/outputDelta"
	entry.native, entry.Agent = &native, s.path(native.thread)
	if view.ended && !view.complete {
		// The report ended early, as when the helper declined after the
		// match; the host's combined output applies from here on.
		if changed {
			run.output.Invalidate()
		}
		if view.semantic {
			return []activityPaneEntry{entry}, false
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
	native.segments = view.segments
	u.holdCommand(entry, activityui.CommandFrame{Output: frame, Segments: commandPresentation(entry).Segments})
	u.dirty = true
	if !view.semantic {
		return nil, true
	}
	native.segments = slices.Clone(view.segments)
	for i := range native.segments {
		native.segments[i].tail, native.segments[i].omit = nil, 0
	}
	return []activityPaneEntry{entry}, true
}

// reconcileTrackedCommand admits segment outcomes when the report ended with
// the host's own exit status. Otherwise the report is incomplete, and the
// already-published host aggregate remains authoritative.
func (u *appServerUI) reconcileTrackedCommand(key [3]string, entry activityPaneEntry, item observation.Item) (activityPaneEntry, bool) {
	run := u.session.commands[key]
	view, _ := u.commandReportView(key, run, true, appServerCommandDirectory(item, u.session.cwd))
	if !run.entry.native.invocation.ReportMatchesHost() || len(view.segments) == 0 {
		return entry, false
	}
	native := *entry.native
	native.segments, native.segmentsPending = view.segments, false
	u.retainCommandSegments(entry, item, view)
	for i := range native.segments {
		// A commit reads its object from the directory the host ran it in.
		if segment := &native.segments[i]; segment.raw != "" && segment.changes == nil {
			segment.changes, segment.commit = activitypresentation.VCSOutputRows(toolActivityShellSource(appServerDisplayCommand(segment.source)), vcsSegmentCwd(item.Cwd, native.segments[:i]), segment.raw)
		}
	}
	// Successful segments' output stays open until the agent's next event.
	entry.completed = entry.native.commandEnded
	entry.native = &native
	if !view.output {
		entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
		native.changes, native.commit = commandOutputRows(item)
	}
	return entry, true
}

// settleCommandReport updates attribution on the existing completed record. This is
// separate from native merging, which must continue rejecting late host deltas.
func (v *activityAdapter) settleCommandReport(enriched activityPaneEntry, valid bool) {
	for i, record := range v.Entries {
		if !enriched.native.sameItem(record.Source.native) {
			continue
		}
		entry := record.Source
		native := *entry.native
		native.segments, native.segmentsPending = nil, false
		entry.native = &native
		values := record.Values
		if valid {
			native.segments, native.changes, native.commit = enriched.native.segments, enriched.native.changes, enriched.native.commit
			entry.completed = enriched.completed
			entry.outputTail, entry.outputOmit = enriched.outputTail, enriched.outputOmit
			values = activitypresentation.RetainAnnotations(activityContent(entry).Values(), values)
		}
		v.Model.Replace(i, entry, values)
		return
	}
}
