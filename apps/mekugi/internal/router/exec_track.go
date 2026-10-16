package router

import (
	"context"
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"

	"mvdan.cc/sh/v3/syntax"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Codex can wait 150 ms for early process exit before reporting the command
// item. Allow that grace period plus notification delivery time, while staying
// below the helper's 500 ms reply timeout. An unmatched shell then runs untracked.
const execTrackClaimWait = 350 * time.Millisecond

// A completed command waits this long for its report to end before it
// falls back to the combined host output.
const execTrackCompletionWait = 300 * time.Millisecond

// execInstrumentation contains only authenticated frontend guard paths.
// Report transport location conveys no authority to recognize instrumentation.
type execInstrumentation struct{ vcs, sudo string }

// execTrackHub receives the segment reports of tracked command shells and
// matches each to the live Codex command item that started it. It only
// observes: execution, output, and exit status stay with Codex and its shell.
type execTrackHub struct {
	transport       *segmenttransport.Server
	instrumentation execInstrumentation
	mu              sync.Mutex
	started         []execTrackCommand // Live, unmatched command items, oldest first.
	changed         chan struct{}      // Closed and replaced at command/segment lifecycle boundaries.
	tracks          map[[3]string]*execTrack
	previews        map[*execPreviewTrack]struct{} // Registered only for a live preview's lifetime.
	sequence        uint64                         // Host starts within this router lifetime.
	closed          bool
}

type execTrackCommand struct {
	key    [3]string // Thread, turn, item.
	script string
	serial uint64
}

// execTrack is one command's report, guarded by the hub.
type execTrack struct {
	script   string
	hostOnly bool   // Native command identity, before any segment report.
	serial   uint64 // Host start, before the helper claims this item.
	segments []execTrackSegment
	created  []segmenttransport.Message
	terminal bool // Output stays on the terminal; only statuses are reported.
	lossy    bool // Complete segment output is unavailable.
	ended    bool // Report processing stopped, including transport failure.
	closed   bool // The helper reported that all output was sent.
	done     bool // The shell finished its script.
	code     int  // The shell's exit status, once done.
	dirty    bool
}

type execTrackSegment struct {
	timing  segmenttransport.Timing
	source  string
	edit    bool   // The shared classifier identifies an edit operation.
	text    string // Display operations, derived once by the UI.
	instant bool   // Its operations show output at once rather than rolling.
	began   bool
	ended   bool
	code    int
	output  activityui.OutputTail
	fresh   []byte                // Output since the last view, bounded like a retained output.
	vcs     bool                  // A VCS command whose raw output becomes change rows.
	raw     []byte                // A VCS segment's unsanitized output, bounded like fresh.
	full    *activitymodel.Output // Retained by the UI, which alone reads and writes it.
}

func attachExecTrack(server *segmenttransport.Server, instrumentation execInstrumentation) (*execTrackHub, error) {
	h := &execTrackHub{instrumentation: instrumentation, changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack)}
	err := server.Register(func(ctx context.Context, hello segmenttransport.Message) segmenttransport.Report {
		key, track, ok := h.claim(ctx, hello)
		if !ok {
			return nil
		}
		return &execTrackStream{hub: h, key: key, track: track}
	})
	if err != nil {
		return nil, err
	}
	h.transport = server
	return h, nil
}

func (h *execTrackHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.transport.Close()
}

// detachOutputs ends presentation retention without ending host reporting.
// A later attachment must use combined host output, not mistake the remaining
// segment bytes for complete evidence.
func (h *execTrackHub) detachOutputs() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, track := range h.tracks {
		for i := range track.segments {
			segment := &track.segments[i]
			if segment.full != nil {
				segment.full.Release()
				segment.full = nil
				track.lossy, track.dirty = true, true
			}
		}
	}
}

// Match shell reports before queued presentation can exhaust the startup deadline.
func (h *execTrackHub) observeCommandStart(message appserver.Message) {
	if h == nil || message.Method != "item/started" {
		return
	}
	var event appServerEvent
	if json.Unmarshal(message.Params, &event) != nil || event.Item.Type != "commandExecution" {
		return
	}
	item := event.Item.ID
	if event.ItemID != "" {
		item = event.ItemID
	}
	h.start([3]string{event.ThreadID, event.TurnID, item}, event.Item.Command)
}

// start is idempotent when the UI later consumes the same host notification.
func (h *execTrackHub) start(key [3]string, command string) {
	if h == nil {
		return
	}
	script, ok := appServerShellScript(command)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if slices.ContainsFunc(h.started, func(c execTrackCommand) bool { return c.key == key }) || h.tracks[key] != nil {
		return
	}
	h.sequence++
	h.started = append(h.started, execTrackCommand{key: key, script: script, serial: h.sequence})
	for preview := range h.previews {
		preview.retain(key, &execTrack{script: script, serial: h.sequence, hostOnly: true})
	}
	h.signal()
}

// finish forgets a command that completed without a report, and a report
// whose command the UI has finished presenting.
func (h *execTrackHub) finish(key [3]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = slices.DeleteFunc(h.started, func(c execTrackCommand) bool { return c.key == key })
	delete(h.tracks, key)
}

// completed records the nested host command boundary, not its enclosing cell
// or the UI's later output-drain boundary. Failure also ends a live observation.
func (h *execTrackHub) completed(key [3]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for preview := range h.previews {
		for _, match := range preview.matches {
			if match.key == key {
				match.hostCompleted = true
			}
		}
	}
	h.signal()
}

func (h *execTrackHub) tracking(key [3]string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tracks[key] != nil
}

// claim matches a report to a unique live command in its thread running the
// same script. Ambiguous unmatched items run untracked: exchanging reports
// would also exchange their segment lifecycle and preview identity.
func (h *execTrackHub) claim(ctx context.Context, hello segmenttransport.Message) ([3]string, *execTrack, bool) {
	timer := time.NewTimer(execTrackClaimWait)
	defer timer.Stop()
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return [3]string{}, nil, false
		}
		index := -1
		for i, command := range h.started {
			if command.script != hello.Script || hello.Thread != "" && command.key[0] != hello.Thread {
				continue
			}
			if index >= 0 {
				h.mu.Unlock()
				return [3]string{}, nil, false
			}
			index = i
		}
		if index >= 0 {
			command := h.started[index]
			key := command.key
			h.started = slices.Delete(h.started, index, index+1)
			track := &execTrack{script: hello.Script, serial: command.serial, terminal: hello.Terminal, dirty: true}
			for _, source := range hello.Segments {
				track.segments = append(track.segments, execTrackSegment{source: source, edit: execSegmentEdits(source), vcs: activitypresentation.VCSOutputSource(toolActivityShellSource(source))})
			}
			h.tracks[key] = track
			for preview := range h.previews {
				preview.retain(key, track)
			}
			h.signal()
			h.mu.Unlock()
			return key, track, true
		}
		changed := h.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			return [3]string{}, nil, false
		case <-ctx.Done():
			return [3]string{}, nil, false
		}
	}
}

type execTrackStream struct {
	hub   *execTrackHub
	key   [3]string
	track *execTrack
}

func (s *execTrackStream) Observe(message segmenttransport.Message) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.track.apply(message)
	if message.Type != segmenttransport.Output {
		s.hub.signal()
	}
}
func (s *execTrackStream) End() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.track.ended, s.track.dirty = true, true
	s.hub.signal()
}
func (s *execTrackStream) Abort() { s.hub.finish(s.key) }

func (h *execTrackHub) signal() {
	close(h.changed)
	h.changed = make(chan struct{})
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
	for _, block := range toolOperationBlocks(execSegmentText(source)) {
		if slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
			return true
		}
	}
	return false
}

// execPreviewTrack joins a display-only writer window to live segment reports.
// Exact thread, turn and script identity are required; identical concurrent
// commands are ambiguous and must not retire one another's previews.
type execPreviewTrack struct {
	hub          *execTrackHub
	thread, turn string
	matches      map[string]*execPreviewMatch
	order        []string
	streamable   map[string]bool // Optional source-only segment scope.
	after        uint64          // Only host invocations started after this window opened.
}

type execPreviewMatch struct {
	key           [3]string
	track         *execTrack
	ambiguous     bool
	hostCompleted bool
	mayWrite      bool
}

func newExecPreviewTrack(hub *execTrackHub, thread, turn string, commands []commandanalysis.Command) *execPreviewTrack {
	if hub == nil || thread == "" || turn == "" {
		return nil
	}
	p := &execPreviewTrack{hub: hub, thread: thread, turn: turn, matches: make(map[string]*execPreviewMatch)}
	// Completion observation also admits commands without predictable source.
	for _, command := range commands {
		script := p.authoredScript(command.Command, "")
		if previous := p.matches[script]; previous != nil {
			previous.ambiguous = true
		} else {
			plan := execscope.Classify(execscope.Input{Command: command.Command, Directory: command.Workdir, Shell: command.Shell})
			p.matches[script] = &execPreviewMatch{mayWrite: plan.Class != execscope.Neutral && (len(plan.Programs) == 0 || slices.ContainsFunc(plan.Programs, func(program execscope.Program) bool { return program.Direct }))}
			p.order = append(p.order, script)
		}
	}
	if len(p.matches) == 0 {
		return nil
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.previews == nil {
		hub.previews = make(map[*execPreviewTrack]struct{})
	}
	// The report carries no workspace or outer-call identity. Two live
	// windows claiming the same future script cannot both own its report.
	for other := range hub.previews {
		if other.thread != thread || other.turn != turn {
			continue
		}
		for script, match := range p.matches {
			if sibling := other.matches[script]; sibling != nil {
				match.ambiguous, sibling.ambiguous = true, true
			}
		}
	}
	hub.previews[p] = struct{}{}
	p.after = hub.sequence
	return p
}

// retain keeps at most one report per captured script, independent of Activity
// retirement. Repeated matches remain ambiguous, without accumulating reports.
// The hub lock must be held.
func (p *execPreviewTrack) retain(key [3]string, track *execTrack) {
	if key[0] != p.thread || key[1] != p.turn || track.serial <= p.after {
		return
	}
	if match := p.matches[p.authoredScript(track.script, key[2])]; match != nil {
		if match.track != nil && match.key != key {
			match.ambiguous = true
		} else {
			match.key, match.track = key, track
		}
	}
}

// Only the current session's exact guard instrumentation may differ from the
// captured authored source. Host/report matching keeps the executing script.
func (p *execPreviewTrack) authoredScript(script, item string) string {
	// Reapply only observed guards: sudo also runs with the VCS guard disabled.
	var guard, sudo string
	source := shellinstrumentation.DisplayScript(script, func(directory string) bool {
		if directory == "" {
			return false
		}
		switch directory {
		case p.hub.instrumentation.vcs:
			guard = directory
		case p.hub.instrumentation.sudo:
			sudo = directory
		default:
			return false
		}
		return true
	})
	if source == script {
		return script
	}
	// This owner has no authenticated helper executable. An inverse rewrite
	// with no helper must match exactly, so only injected PATH prefixes qualify.
	rewritten, err := shellinstrumentation.RewriteCommands(source, "", guard, sudo, "")
	if err != nil || rewritten != script {
		if item == "" {
			return script
		}
		// The host hook also binds the guard to this exact command item.
		rewritten, err = shellinstrumentation.RewriteCommands(source, "", guard, sudo, item)
		if err != nil || rewritten != script {
			return script
		}
	}
	return source
}

func (p *execPreviewTrack) close() {
	if p != nil {
		p.hub.mu.Lock()
		delete(p.hub.previews, p)
		p.hub.mu.Unlock()
	}
}

func (p *execPreviewTrack) createdPaths() []segmenttransport.Message {
	if p == nil {
		return nil
	}
	p.hub.mu.Lock()
	defer p.hub.mu.Unlock()
	var paths []segmenttransport.Message
	for _, match := range p.matches {
		if !match.ambiguous && match.track != nil {
			paths = append(paths, match.track.created...)
		}
	}
	return paths
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
	h := p.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	state.changed, state.settled = h.changed, true
	allTracked, writing := true, false
	for _, script := range p.order {
		retained := p.matches[script]
		if retained.ambiguous {
			return execPreviewState{tracked: true, changed: h.changed}
		}
		match := retained.track
		matches := 0
		if match != nil {
			matches++
		}
		for _, pending := range h.started {
			if pending.key[0] == p.thread && pending.key[1] == p.turn && pending.serial > p.after && pending.key != retained.key && p.authoredScript(pending.script, pending.key[2]) == script {
				matches++
			}
		}
		if matches > 1 {
			return execPreviewState{tracked: true, changed: h.changed}
		}
		streamable := p.streamable == nil || p.streamable[script]
		if match != nil && retained.hostCompleted {
			state.completed++
			for _, segment := range match.segments {
				if segment.ended {
					state.completed++
				}
			}
			state.calls = append(state.calls, retained.key[2])
			state.tracked = state.tracked || streamable
			continue
		}
		if match == nil || match.hostOnly || match.ended && !match.done {
			allTracked = allTracked && !streamable
			writing = writing || match != nil && retained.mayWrite
			continue
		}
		state.tracked = state.tracked || streamable
		last, completed := match.lastStarted(), 0
		for i, segment := range match.segments {
			if segment.ended {
				completed++
			}
			writer := segment.edit
			if p.streamable != nil {
				writer = p.streamable[p.authoredScript(segment.source, "")]
			}
			if streamable && writer && !segment.ended && !match.done && (segment.began || i > last) {
				state.settled = false
			}
		}
		// A receipt covers observed endpoints of this invocation. A later
		// command in the same shell does not hold its earlier edits hostage.
		// A sibling with no completed boundary cannot lend its writes to it.
		writing = writing || completed == 0 && last >= 0 && retained.mayWrite && !match.done
		if completed > 0 {
			state.completed += completed
			state.calls = append(state.calls, retained.key[2])
		}
	}
	state.tracked = state.tracked && allTracked
	state.settled = state.tracked && state.settled
	if writing {
		state.completed, state.calls = 0, nil
	}
	return state
}

// Top-level list segments execute in source order. Beginning a later one
// proves that an earlier, never-started operand was short-circuited.
func (t *execTrack) lastStarted() int {
	for i := len(t.segments) - 1; i >= 0; i-- {
		if t.segments[i].began || t.segments[i].ended {
			return i
		}
	}
	return -1
}

func (t *execTrack) apply(message segmenttransport.Message) {
	valid := message.Index >= 0 && message.Index < len(t.segments)
	switch message.Type {
	case segmenttransport.Created:
		if valid && filepath.IsAbs(message.Data) {
			t.created = append(t.created, message)
		}
	case segmenttransport.Begin:
		if valid {
			t.segments[message.Index].began = true
			t.segments[message.Index].timing = segmenttransport.Timing{Started: message.Timing.Started}
		}
	case segmenttransport.Output:
		if valid && !t.lossy {
			segment := &t.segments[message.Index]
			segment.output.Write(message.Data)
			segment.fresh = append(segment.fresh, message.Data...)
			if segment.vcs {
				segment.raw = append(segment.raw, message.Data...)
			}
			if over := max(len(segment.fresh), len(segment.raw)) - activitymodel.OutputBytes; over > 0 {
				// Without a complete pending stream, use the host aggregate
				// rather than splice unrelated byte ranges into retained output.
				segment.fresh, segment.raw = nil, nil
				t.lossy = true
			}
		}
	case segmenttransport.End:
		if valid && message.Code != nil {
			segment := &t.segments[message.Index]
			segment.began, segment.ended, segment.code = true, true, *message.Code
			if !message.Timing.Started.IsZero() && message.Timing.Started.Equal(segment.timing.Started) && !message.Timing.Ended.IsZero() && message.Timing.ElapsedNS >= 0 {
				segment.timing = message.Timing
			}
		}
	case segmenttransport.Done:
		if message.Code != nil {
			t.done, t.code = true, *message.Code
		}
	case segmenttransport.Closed:
		t.closed = true
	case segmenttransport.Lossy:
		t.lossy = true
	default:
		return
	}
	t.dirty = true
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
func (h *execTrackHub) view(key [3]string, final bool, text func(string) string, outputs *activitymodel.Retention) (execTrackView, bool) {
	if h == nil {
		return execTrackView{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	track := h.tracks[key]
	if track == nil {
		return execTrackView{}, false
	}
	changed := track.dirty
	complete := track.ended && track.closed && track.done
	view := execTrackView{output: !track.terminal && len(track.segments) != 1 && !track.lossy && !(track.ended && !complete), ended: track.ended, complete: complete, code: track.code}
	last := track.lastStarted()
	for i := range track.segments {
		segment := &track.segments[i]
		if !view.output {
			segment.fresh = nil
			if segment.full != nil {
				segment.full.Release()
				segment.full = nil
			}
		}
		if segment.text == "" {
			segment.text = text(segment.source)
			segment.instant = instantOperations(segment.text)
		}
		rolled := segment.output.Roll
		if segment.instant {
			rolled = segment.output.Flush
		}
		if rolled() {
			changed = true
		}
		shown := commandSegment{timing: segment.timing, text: segment.text, exit: segment.code, source: segment.source}
		switch {
		case segment.began && !segment.ended && final:
			shown.exit = track.code
		case segment.began && !segment.ended:
			shown.running = true
		case !segment.began && (final || i < last):
			shown.skipped = true
		case !segment.began:
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
				shown.changes = mchangesOutputRows(appServerItem{Command: segment.source, AggregatedOutput: &output})
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
	track.dirty = false
	return view, changed
}

// execSegmentText is a segment's display operations, using the same shell
// classifier as an untracked command.
func execSegmentText(source string) string {
	return execSegmentTextInDirectory(source, "")
}

func execSegmentTextInDirectory(source, cwd string) string {
	if text := toolActivityShellInDirectory(source, cwd); strings.TrimSpace(text) != "" {
		return text
	}
	return toolActivityUnclassifiedShell(source)
}

// flushTrackedCommand re-sends a tracked command whose segments changed, and
// completes it once its report ends. It reports false for an untracked
// command, which keeps the host's single output stream.
func (u *appServerUI) flushTrackedCommand(key [3]string, run *appServerCommandRun) ([]activityPaneEntry, bool) {
	s := &u.session
	if run.completion != nil {
		view, _ := u.execTrack.view(key, false, func(source string) string {
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
		u.execTrack.finish(key)
		return done, true
	}
	view, changed := u.execTrack.view(key, false, func(source string) string {
		return execSegmentTextInDirectory(source, run.entry.native.commandCwd)
	}, s.activity.Outputs(&s.state))
	if !u.execTrack.tracking(key) {
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
	view, _ := u.execTrack.view(key, true, func(source string) string {
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
