package router

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Codex reports a command item just after it spawns the shell, which is
// still reading its startup files. A report waits this long for its item
// before the shell runs the script untracked.
const execTrackClaimWait = 150 * time.Millisecond

// A completed command waits this long for its report to end before it
// falls back to the combined host output.
const execTrackCompletionWait = 300 * time.Millisecond

// ExecTrackPaths names the session-private socket that receives segment
// reports and the directory where the helper keeps per-command files.
func ExecTrackPaths(frontendDirectory string) (socket, directory string) {
	root := filepath.Dir(frontendDirectory)
	return filepath.Join(root, "exec.sock"), filepath.Join(root, "exec")
}

// execTrackHub receives the segment reports of tracked command shells and
// matches each to the live Codex command item that started it. It only
// observes: execution, output, and exit status stay with Codex and its shell.
type execTrackHub struct {
	listener net.Listener
	mu       sync.Mutex
	started  []execTrackCommand // Live, unmatched command items, oldest first.
	changed  chan struct{}      // Closed and replaced when a command starts.
	tracks   map[[3]string]*execTrack
	closed   bool
	wg       sync.WaitGroup
}

type execTrackCommand struct {
	key    [3]string // Thread, turn, item.
	script string
}

// execTrack is one command's report, guarded by the hub.
type execTrack struct {
	segments []execTrackSegment
	terminal bool // Output stays on the terminal; only statuses are reported.
	lossy    bool // Output reports stopped at the helper's bound.
	ended    bool // The report connection closed.
	done     bool // The shell finished its script.
	code     int  // The shell's exit status, once done.
	dirty    bool
}

type execTrackSegment struct {
	source string
	text   string // Display operations, derived once by the UI.
	began  bool
	ended  bool
	code   int
	output activityui.OutputTail
}

func listenExecTrack(ctx context.Context, socket, directory string) (*execTrackHub, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	h := &execTrackHub{listener: listener, changed: make(chan struct{}), tracks: make(map[[3]string]*execTrack)}
	h.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			h.wg.Go(func() { h.serve(ctx, conn) })
		}
	})
	context.AfterFunc(ctx, h.close)
	return h, nil
}

func (h *execTrackHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()
	h.listener.Close()
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
	h.started = append(h.started, execTrackCommand{key: key, script: script})
	close(h.changed)
	h.changed = make(chan struct{})
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

func (h *execTrackHub) tracking(key [3]string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tracks[key] != nil
}

// claim matches a report to the oldest live command in its thread running
// the same script. Identical concurrent scripts are indistinguishable, and
// whichever report claims first takes the oldest item; their segments are
// the same, so only the reported outputs could be exchanged.
func (h *execTrackHub) claim(ctx context.Context, hello execsegment.Message) ([3]string, *execTrack, bool) {
	timer := time.NewTimer(execTrackClaimWait)
	defer timer.Stop()
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return [3]string{}, nil, false
		}
		index := slices.IndexFunc(h.started, func(c execTrackCommand) bool {
			return c.script == hello.Script && (hello.Thread == "" || c.key[0] == hello.Thread)
		})
		if index >= 0 {
			key := h.started[index].key
			h.started = slices.Delete(h.started, index, index+1)
			track := &execTrack{terminal: hello.Terminal, dirty: true}
			for _, source := range hello.Segments {
				track.segments = append(track.segments, execTrackSegment{source: source})
			}
			h.tracks[key] = track
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

func (h *execTrackHub) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	reader := bufio.NewReaderSize(conn, 64<<10)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return
	}
	var hello execsegment.Message
	if line, err := reader.ReadBytes('\n'); err != nil || json.Unmarshal(line, &hello) != nil {
		return
	}
	if hello.Type != execsegment.Hello || hello.Version != execsegment.Protocol || len(hello.Segments) < 2 {
		return
	}
	// The segments must be those of the script this router would split, or
	// the display would attribute statuses to the wrong commands.
	segments, ok := execsegment.Split(hello.Script)
	if !ok || len(segments) != len(hello.Segments) {
		_ = writeExecTrackReply(conn, false)
		return
	}
	for i, segment := range segments {
		if segment.Source != hello.Segments[i] {
			_ = writeExecTrackReply(conn, false)
			return
		}
	}
	key, track, ok := h.claim(ctx, hello)
	if err := writeExecTrackReply(conn, ok); err != nil || !ok {
		if ok {
			h.finish(key)
		}
		return
	}
	defer func() {
		h.mu.Lock()
		track.ended, track.dirty = true, true
		h.mu.Unlock()
	}()
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
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
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func writeExecTrackReply(conn net.Conn, ok bool) error {
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
		}
	case execsegment.Output:
		if valid && !t.lossy {
			t.segments[message.Index].output.Write(message.Data)
		}
	case execsegment.End:
		if valid && message.Code != nil {
			segment := &t.segments[message.Index]
			segment.began, segment.ended, segment.code = true, true, *message.Code
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
	text    string // Display operations for the segment's source.
	running bool
	skipped bool // The list short-circuited before it.
	exit    int
	tail    []string
	omit    int
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
// shell finished is the one the shell exited in.
func (h *execTrackHub) view(key [3]string, final bool, text func(string) string) (execTrackView, bool) {
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
	view := execTrackView{output: !track.terminal && !track.lossy, ended: track.ended, complete: track.ended && track.done, code: track.code}
	for i := range track.segments {
		segment := &track.segments[i]
		if segment.text == "" {
			segment.text = text(segment.source)
		}
		if segment.output.Roll() {
			changed = true
		}
		shown := commandSegment{text: segment.text, exit: segment.code}
		switch {
		case segment.began && !segment.ended && final:
			shown.exit = track.code
		case segment.began && !segment.ended:
			shown.running = true
		case !segment.began && final:
			shown.skipped = true
		case !segment.began:
			continue
		}
		if view.output {
			if final {
				segment.output.Reveal(segment.output.Pending())
			}
			shown.tail, shown.omit = segment.output.Lines()
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
		view, _ := u.execTrack.view(key, false, execSegmentText)
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
	view, changed := u.execTrack.view(key, false, execSegmentText)
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
	rolled := !view.output && run.output.Roll()
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
	view, _ := u.execTrack.view(key, true, execSegmentText)
	if !view.complete || item.ExitCode == nil || view.code != *item.ExitCode || len(view.segments) == 0 {
		return u.session.commandDone(entry, item, now)
	}
	native := *entry.native
	native.segments = view.segments
	// Successful segments' output stays open until the agent's next event.
	native.settled, native.collapsed = now, false
	entry.native = &native
	if !view.output {
		entry.outputTail, entry.outputOmit = appServerOutputTail(item.AggregatedOutput)
	}
	return []activityPaneEntry{entry}
}
