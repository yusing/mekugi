package router

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"

	"mvdan.cc/sh/v3/syntax"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const execTrackCompletionWait = 300 * time.Millisecond

// Activity output belongs to its existing application command scope, not the
// invocation observer. Replacing that scope releases output without reviving it.
type execTrackPresentation struct {
	segments []execSegmentOutput
	version  uint64
}

type execSegmentOutput struct {
	text         string
	instant, vcs bool
	output       activityui.OutputTail
	raw, fresh   []byte
	full         *activitymodel.Output
}

func (p *execTrackPresentation) release() bool {
	released := false
	for i := range p.segments {
		segment := &p.segments[i]
		if segment.full != nil {
			segment.full.Release()
			segment.full = nil
			released = true
		}
	}
	return released
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
	segments []commandSegment
	output   bool // Segments carry their own output; otherwise the host's combined output applies.
	ended    bool // The report connection closed.
	complete bool // The report ended with the shell's own exit status.
	code     int
}

// view reports the command's segments, and whether they changed since the
// last view. While live, a segment appears once it starts. Final views mark
// segments the list never reached as skipped; a segment still open when the
// shell finished is the one the shell exited in. Segment output is retained
// in outputs, when given, on the UI goroutine that views it.
func (p *execTrackPresentation) view(observer *segments.Observer, key [3]string, final bool, text func(string) string, outputs *activitymodel.Retention) (execTrackView, bool) {
	track, ok := observer.Read(key)
	if !ok {
		return execTrackView{}, false
	}
	changed := p.version != track.Version
	p.version = track.Version
	for len(p.segments) < len(track.Segments) {
		p.segments = append(p.segments, execSegmentOutput{})
	}
	for i, observed := range track.Segments {
		segment := &p.segments[i]
		if segment.text == "" {
			segment.text = text(observed.Source)
			segment.instant = instantOperations(segment.text)
			segment.vcs = activitypresentation.VCSOutputSource(toolActivityShellSource(observed.Source))
		}
		if !track.Lossy {
			segment.output.Write(observed.Output)
			segment.fresh = append(segment.fresh, observed.Output...)
			if segment.vcs {
				segment.raw = append(segment.raw, observed.Output...)
			}
			if max(len(segment.raw), len(segment.fresh)) > activitymodel.OutputBytes {
				segment.raw, segment.fresh = nil, nil
				track.Lossy = true
				observer.ReleaseOutput(key)
			}
		}
	}
	complete := track.Complete()
	view := execTrackView{output: !track.Terminal && len(track.Segments) != 1 && !track.Lossy && !(track.Ended && !complete), ended: track.Ended, complete: complete, code: track.Code}
	last := track.LastStarted()
	for i, observed := range track.Segments {
		segment := &p.segments[i]
		if !view.output {
			segment.fresh = nil
			if segment.full != nil {
				segment.full.Release()
				segment.full = nil
			}
		}
		rolled := segment.output.Roll
		if segment.instant {
			rolled = segment.output.Flush
		}
		if rolled() {
			changed = true
		}
		shown := commandSegment{timing: observed.Timing, text: segment.text, exit: observed.Code, source: observed.Source}
		switch {
		case observed.Began && !observed.Ended && final:
			shown.exit = track.Code
		case observed.Began && !observed.Ended:
			shown.running = true
		case !observed.Began && (final || i < last):
			shown.skipped = true
		case !observed.Began:
			continue
		}
		if view.output {
			if final {
				segment.output.Flush()
			}
			if outputs != nil && !shown.skipped {
				if segment.full == nil {
					segment.full = outputs.New()
				}
				segment.full.Write(string(segment.fresh))
				segment.fresh = segment.fresh[:0]
				if !shown.running {
					segment.full.Finish(nil, &shown.exit)
				}
				shown.output = segment.full
			}
			shown.tail, shown.omit = segment.output.Lines()
			if final && !shown.skipped && shown.exit == 0 {
				output := strings.Join(shown.tail, "\n")
				shown.changes = mchangesOutputRows(appServerItem{Command: observed.Source, AggregatedOutput: &output})
				if len(shown.changes) > 0 && shown.omit > 0 {
					shown.changes = append([]activitypresentation.ChangeRow{{Note: activityui.Elision{Hidden: shown.omit}.Text()}}, shown.changes...)
				}
				if segment.vcs {
					shown.raw = string(segment.raw)
				}
			}
		}
		view.segments = append(view.segments, shown)
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
		view, _ := run.segments.view(u.execTrack, key, false, func(source string) string {
			return execSegmentTextInDirectory(source, run.entry.native.commandCwd)
		}, s.activity.Outputs(&s.state))
		if !view.ended && time.Since(run.completedAt) < execTrackCompletionWait {
			return nil, true
		}
		done := u.trackedCommandDone(key, *run.completion, run.completed)
		for i := range done {
			done[i].Agent = s.path(key[0]) // The thread may have been renamed.
		}
		delete(s.commands, key)
		u.execTrack.Forget(key)
		return done, true
	}
	view, changed := run.segments.view(u.execTrack, key, false, func(source string) string {
		return execSegmentTextInDirectory(source, run.entry.native.commandCwd)
	}, s.activity.Outputs(&s.state))
	if !u.execTrack.Tracking(key) {
		return nil, false
	}
	if view.ended && !view.complete {
		// The report ended early, as when the helper declined after the
		// match; the host's combined output applies from here on.
		run.dirty = run.dirty || changed
		return nil, false
	}
	// Terminal and over-bound commands keep the host's combined output.
	rolled := !view.output && run.roll()
	if !changed && !rolled && !run.dirty {
		return nil, true
	}
	run.dirty = false
	entry := run.entry
	native := *entry.native
	native.phase = "item/commandExecution/outputDelta"
	native.segments = view.segments
	entry.native, entry.Agent = &native, s.path(native.thread)
	entry.outputTail, entry.outputOmit = nil, 0
	if !view.output {
		entry.outputTail, entry.outputOmit = run.output.Lines()
	}
	return []activityPaneEntry{entry}, true
}

// trackedCommandDone shows each segment's outcome when the report ended with
// the host's own exit status. Otherwise the report is incomplete, and the
// command falls back to the host's combined result.
func (u *appServerUI) trackedCommandDone(key [3]string, entry activityPaneEntry, item appServerItem) []activityPaneEntry {
	now := time.Now()
	run := u.session.commands[key]
	view, _ := run.segments.view(u.execTrack, key, true, func(source string) string {
		return execSegmentTextInDirectory(source, appServerCommandDirectory(item, u.session.cwd))
	}, u.session.activity.Outputs(&u.session.state))
	if !view.complete || item.ExitCode == nil || view.code != *item.ExitCode || len(view.segments) == 0 {
		return u.session.commandDone(entry, item, now)
	}
	native := *entry.native
	native.segments = view.segments
	u.retainCommandSegments(entry, item, view)
	for i := range native.segments {
		// A commit reads its object from the directory the host ran it in.
		if segment := &native.segments[i]; segment.raw != "" && segment.changes == nil {
			segment.changes, segment.commit = activitypresentation.VCSOutputRows(toolActivityShellSource(appServerDisplayCommand(segment.source)), vcsSegmentCwd(item.Cwd, native.segments[:i]), segment.raw)
		}
	}
	// Successful segments' output stays open until the agent's next event.
	entry.completed = now
	entry.native = &native
	if !view.output {
		entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
		native.changes, native.commit = commandOutputRows(item)
	}
	return []activityPaneEntry{entry}
}
