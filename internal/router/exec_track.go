package router

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/vcsguard"
	"golang.org/x/sys/unix"
	"mvdan.cc/sh/v3/syntax"
)

// Codex can wait 150 ms for early process exit before reporting the command
// item. Allow that grace period plus notification delivery time, while staying
// below the helper's 500 ms reply timeout. An unmatched shell then runs untracked.
const execTrackClaimWait = 350 * time.Millisecond

// A completed command waits this long for its report to end before it
// falls back to the combined host output.
const execTrackCompletionWait = 300 * time.Millisecond

// ExecTrackPaths names the session-private request FIFO that receives segment
// requests and the directory where the router creates per-command files.
func ExecTrackPaths(frontendDirectory string) (channel, directory string) {
	root := filepath.Dir(frontendDirectory)
	return filepath.Join(root, "exec.requests"), filepath.Join(root, "exec")
}

// execTrackHub receives the segment reports of tracked command shells and
// matches each to the live Codex command item that started it. It only
// observes: execution, output, and exit status stay with Codex and its shell.
type execTrackHub struct {
	requests  *os.File
	directory string
	mu        sync.Mutex
	started   []execTrackCommand // Live, unmatched command items, oldest first.
	changed   chan struct{}      // Closed and replaced at command/segment lifecycle boundaries.
	tracks    map[[3]string]*execTrack
	previews  map[*execPreviewTrack]struct{} // Registered only for a live preview's lifetime.
	// approvals carries guarded remote writes to the UI, which alone receives.
	approvals       chan *vcsApproval
	approvalTimeout time.Duration // Denies an unanswered write; tests shorten it.
	guardClose      func()        // Closes the approval socket and removes its owned resources.
	sequence        uint64        // Host starts within this router lifetime.
	closed          bool
	wg              sync.WaitGroup
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
	terminal bool // Output stays on the terminal; only statuses are reported.
	lossy    bool // Output reports stopped at the helper's bound.
	ended    bool // The report connection closed.
	done     bool // The shell finished its script.
	code     int  // The shell's exit status, once done.
	dirty    bool
}

type execTrackSegment struct {
	timing  execsegment.Timing
	source  string
	edit    bool   // The shared classifier identifies an edit operation.
	text    string // Display operations, derived once by the UI.
	instant bool   // Its operations show output at once rather than rolling.
	began   bool
	ended   bool
	code    int
	output  activityui.OutputTail
	fresh   []byte             // Output since the last view, bounded like a retained output.
	vcs     bool               // A VCS command whose raw output becomes change rows.
	raw     []byte             // A VCS segment's unsanitized output, bounded like fresh.
	full    *activityui.Output // Retained by the UI, which alone reads and writes it.
}

func listenExecTrack(ctx context.Context, channel, directory string) (*execTrackHub, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(channel)
	if err := unix.Mkfifo(channel, 0o600); err != nil {
		return nil, &os.PathError{Op: "mkfifo", Path: channel, Err: err}
	}
	// Hold a write end so idle reads do not end.
	requests, err := os.OpenFile(channel, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	h := &execTrackHub{requests: requests, directory: directory, changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack), approvals: make(chan *vcsApproval), approvalTimeout: vcsguard.Timeout}
	h.wg.Go(func() {
		reader := bufio.NewReader(requests)
		for {
			line, err := reader.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = reader.ReadSlice('\n')
				}
				continue
			}
			if err != nil {
				return
			}
			if work, ok := execsegment.ReportDirectory(h.directory, strings.TrimSuffix(string(line), "\n")); ok {
				h.wg.Go(func() { h.acceptReport(ctx, work) })
			}
		}
	})
	context.AfterFunc(ctx, h.close)
	return h, nil
}

// acceptReport creates all command files outside Codex's sandbox. The helper
// connects by opening these existing FIFOs, even with a read-only mount.
func (h *execTrackHub) acceptReport(ctx context.Context, work string) {
	if err := os.Mkdir(work, 0o700); err != nil {
		return
	}
	defer os.RemoveAll(work)
	for _, name := range []string{"report", "reply", "out", "err"} {
		if err := unix.Mkfifo(filepath.Join(work, name), 0o600); err != nil {
			return
		}
	}
	fd, err := unix.Open(filepath.Join(work, "report"), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	report := os.NewFile(uintptr(fd), filepath.Join(work, "report"))
	defer report.Close()
	// The helper opens report for writing before reply for reading. Once
	// reply has a reader, report cannot return a premature EOF.
	deadline := time.Now().Add(time.Second)
	for {
		fd, err = unix.Open(filepath.Join(work, "reply"), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.ENXIO) || time.Now().After(deadline) || ctx.Err() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	reply := os.NewFile(uintptr(fd), filepath.Join(work, "reply"))
	defer reply.Close()
	h.serve(ctx, report, reply, work)
}

func (h *execTrackHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	guard := h.guardClose
	h.mu.Unlock()
	if h.requests != nil {
		h.requests.Close()
	}
	if guard != nil {
		guard()
	}
}

// start makes a live command item available to its shell's report.
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
func (h *execTrackHub) claim(ctx context.Context, hello execsegment.Message) ([3]string, *execTrack, bool) {
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
				track.segments = append(track.segments, execTrackSegment{source: source, edit: execSegmentEdits(source), vcs: vcsSegment(source)})
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

func (h *execTrackHub) serve(ctx context.Context, report, reply *os.File, work string) {
	stop := context.AfterFunc(ctx, func() { report.Close(); reply.Close() })
	defer stop()
	reader := bufio.NewReaderSize(report, 64<<10)
	if err := report.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	var hello execsegment.Message
	if line, err := reader.ReadBytes('\n'); err != nil || json.Unmarshal(line, &hello) != nil {
		return
	}
	if hello.Type != execsegment.Hello || hello.Version != execsegment.Protocol || len(hello.Segments) == 0 {
		return
	}
	// The segments must be those of the script this router would split, or
	// the display would attribute statuses to the wrong commands.
	segments, ok := execsegment.Split(hello.Script)
	if !ok || len(segments) != len(hello.Segments) {
		_ = writeExecTrackReply(reply, false)
		return
	}
	for i, segment := range segments {
		if segment.Source != hello.Segments[i] {
			_ = writeExecTrackReply(reply, false)
			return
		}
	}
	if len(segments) > 1 {
		if err := os.WriteFile(filepath.Join(work, "script"), []byte(execsegment.Rewrite(hello.Script, segments)), 0o600); err != nil {
			return
		}
	}
	key, track, ok := h.claim(ctx, hello)
	if err := writeExecTrackReply(reply, ok); err != nil || !ok {
		if ok {
			h.finish(key)
		}
		return
	}
	defer func() {
		h.mu.Lock()
		track.ended, track.dirty = true, true
		h.signal()
		h.mu.Unlock()
	}()
	if err := report.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var message execsegment.Message
			if json.Unmarshal(line, &message) != nil {
				return
			}
			h.mu.Lock()
			track.apply(message)
			if message.Type != execsegment.Output {
				h.signal()
			}
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// signal wakes lifecycle consumers. The hub lock must be held.
func (h *execTrackHub) signal() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func execSegmentEdits(source string) bool {
	// Formatters and file copies can be shown as Run rather than Edit.
	// Their write segments still own a live edit's lifetime, including binary
	// copies for which text projection cannot establish completion.
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "")
	writes := false
	if err == nil {
		syntax.Walk(program, func(node syntax.Node) bool {
			if _, declaration := node.(*syntax.FuncDecl); declaration {
				return false
			}
			if call, ok := node.(*syntax.CallExpr); ok {
				if words, literal := literalArgs(call.Args); literal && len(words) > 0 {
					name := filepath.Base(words[0])
					writes = writes || name == "cp" || name == "install" || execGoFormatterWrites(name, words[1:])
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
	after        uint64 // Only host invocations started after this window opened.
}

type execPreviewMatch struct {
	key           [3]string
	track         *execTrack
	ambiguous     bool
	hostCompleted bool
}

func newExecPreviewTrack(hub *execTrackHub, thread, turn string, commands []execCommandInput) *execPreviewTrack {
	if hub == nil || thread == "" || turn == "" {
		return nil
	}
	p := &execPreviewTrack{hub: hub, thread: thread, turn: turn, matches: make(map[string]*execPreviewMatch)}
	for _, command := range commands {
		if execSegmentEdits(command.Command) {
			if previous := p.matches[command.Command]; previous != nil {
				previous.ambiguous = true
			} else {
				p.matches[command.Command] = &execPreviewMatch{}
			}
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
	if match := p.matches[track.script]; match != nil {
		if match.track != nil && match.key != key {
			match.ambiguous = true
		} else {
			match.key, match.track = key, track
		}
	}
}

func (p *execPreviewTrack) close() {
	if p != nil {
		p.hub.mu.Lock()
		delete(p.hub.previews, p)
		p.hub.mu.Unlock()
	}
}

func (p *execPreviewTrack) state() (tracked, settled bool, changed <-chan struct{}) {
	if p == nil {
		return false, false, nil
	}
	h := p.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	changed = h.changed
	settled = true
	allTracked := true
	for script, retained := range p.matches {
		if retained.ambiguous {
			return true, false, changed
		}
		match := retained.track
		matches := 0
		if match != nil {
			matches++
		}
		for _, pending := range h.started {
			if pending.key[0] == p.thread && pending.key[1] == p.turn && pending.script == script && pending.serial > p.after && pending.key != retained.key {
				matches++
			}
		}
		if matches > 1 {
			return true, false, changed
		}
		if match != nil && retained.hostCompleted {
			tracked = true
			continue
		}
		if match == nil || match.hostOnly || match.ended && !match.done {
			allTracked = false
			continue
		}
		tracked = true
		last := match.lastStarted()
		for i, segment := range match.segments {
			if segment.edit && !segment.ended && !match.done && (segment.began || i > last) {
				settled = false
			}
		}
	}
	return tracked && allTracked, tracked && allTracked && settled, changed
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

func writeExecTrackReply(conn *os.File, ok bool) error {
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	line, err := json.Marshal(execsegment.Reply{OK: ok})
	if err != nil {
		return err
	}
	_, err = conn.Write(append(line, '\n'))
	return err
}

func (t *execTrack) apply(message execsegment.Message) {
	valid := message.Index >= 0 && message.Index < len(t.segments)
	switch message.Type {
	case execsegment.Begin:
		if valid {
			t.segments[message.Index].began = true
			t.segments[message.Index].timing = execsegment.Timing{Started: message.Timing.Started}
		}
	case execsegment.Output:
		if valid && !t.lossy {
			segment := &t.segments[message.Index]
			segment.output.Write(message.Data)
			segment.fresh = append(segment.fresh, message.Data...)
			if segment.vcs {
				segment.raw = append(segment.raw, message.Data...)
			}
			if over := max(len(segment.fresh), len(segment.raw)) - activityui.OutputBytes; over > 0 {
				// Without a complete pending stream, use the host aggregate
				// rather than splice unrelated byte ranges into retained output.
				segment.fresh, segment.raw = nil, nil
				t.lossy = true
			}
		}
	case execsegment.End:
		if valid && message.Code != nil {
			segment := &t.segments[message.Index]
			segment.began, segment.ended, segment.code = true, true, *message.Code
			if !message.Timing.Started.IsZero() && message.Timing.Started.Equal(segment.timing.Started) && !message.Timing.Ended.IsZero() && message.Timing.ElapsedNS >= 0 {
				segment.timing = message.Timing
			}
		}
	case execsegment.Done:
		if message.Code != nil {
			t.done, t.code = true, *message.Code
		}
	case execsegment.Lossy:
		t.lossy = true
	default:
		return
	}
	t.dirty = true
}

// commandSegment is one segment as the Activity shows it.
type commandSegment struct {
	timing  execsegment.Timing
	text    string // Display operations for the segment's source.
	running bool
	skipped bool // The list short-circuited before it.
	exit    int
	tail    []string
	omit    int
	changes []activityui.ChangeRow
	output  *activityui.Output
	source  string
	raw     string       // A finished VCS segment's complete output, for its change rows.
	commit  gitCommitKey // The commit object whose files complete changes.
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
func (h *execTrackHub) view(key [3]string, final bool, text func(string) string, outputs *activityui.Retention) (execTrackView, bool) {
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
	view := execTrackView{output: !track.terminal && len(track.segments) != 1 && !track.lossy && !(track.ended && !track.done), ended: track.ended, complete: track.ended && track.done, code: track.code}
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
					shown.changes = append([]activityui.ChangeRow{{Note: activityui.Elision{Hidden: shown.omit}.Text()}}, shown.changes...)
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
	if text := toolActivityShell(source); strings.TrimSpace(text) != "" {
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
		view, _ := u.execTrack.view(key, false, execSegmentText, &s.outputs)
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
	view, changed := u.execTrack.view(key, false, execSegmentText, &s.outputs)
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
	view, _ := u.execTrack.view(key, true, execSegmentText, &u.session.outputs)
	if !view.complete || item.ExitCode == nil || view.code != *item.ExitCode || len(view.segments) == 0 {
		return u.session.commandDone(entry, item, now)
	}
	native := *entry.native
	native.segments = view.segments
	u.retainCommandSegments(entry, item, view)
	for i := range native.segments {
		// A commit reads its object from the directory the host ran it in.
		if segment := &native.segments[i]; segment.raw != "" && segment.changes == nil {
			segment.changes, segment.commit = vcsOutputRows(segment.source, vcsSegmentCwd(item.Cwd, native.segments[:i]), segment.raw)
		}
	}
	// Successful segments' output stays open until the agent's next event.
	native.settled, native.collapsed = now, false
	entry.native = &native
	if !view.output {
		entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
		native.changes, native.commit = commandOutputRows(item)
	}
	return []activityPaneEntry{entry}
}
