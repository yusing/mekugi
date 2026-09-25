package router

import (
	"context"
	"fmt"
	uv "github.com/charmbracelet/ultraviolet"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/livediff"
	"golang.org/x/term"
)

// The wrapper owns only terminal presentation. Codex still owns every tool,
// approval, child process and native agent inside its PTY.
type terminalUI struct {
	pasteEnd                          int
	hostReply                         []byte
	hostReplyDiscard, hostReplyEscape bool
	codexScroll                       int
	mouseModes                        map[ansi.Mode]bool
	hostError                         error
	diffFailure                       string
	master                            *os.File
	codex, diffScreen                 *vt.Emulator
	diff                              *liveDiffTerminalController
	agents                            *liveActivityView
	auto                              *autoLiveDiff
	activity                          *subagentActivity
	width, height, split, horizontal  int
	focus, drag                       int // 0 Codex, 1 diff, 2 agents; drag 1 main, 2 auxiliary, 3 files
	side, activityOpen, cursorVisible bool
	prefix                            bool
	sequenceAt                        time.Time
	sequence, agentEscape             string
	paste                             bool
	generation                        uint64
	layout                            terminalLayout
}

type terminalRect struct{ x, y, w, h int }
type terminalLayout struct {
	codex, diff, agents  terminalRect
	vertical, horizontal int
}

func (r terminalRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

func terminalGeometry(width, height, split, horizontal, focus int, side, agents bool) terminalLayout {
	w, h := max(1, width), max(1, height-1)
	l := terminalLayout{vertical: -1, horizontal: -1}
	if !side {
		l.codex = terminalRect{0, 0, w, h}
		return l
	}
	if w < 100 || h < 12 {
		r := terminalRect{0, 0, w, h}
		switch focus {
		case 1:
			l.diff = r
		case 2:
			l.agents = r
		default:
			l.codex = r
		}
		return l
	}
	if split == 0 {
		split = w / 2
	}
	split = min(max(30, split), w-41)
	l.vertical = split
	l.codex = terminalRect{0, 0, split, h}
	l.diff = terminalRect{split + 1, 0, w - split - 1, h}
	if agents {
		if horizontal == 0 {
			horizontal = h * 3 / 5
		}
		horizontal = min(max(4, horizontal), h-5)
		l.horizontal = horizontal
		l.diff.h = horizontal
		l.agents = terminalRect{split + 1, horizontal + 1, w - split - 1, h - horizontal - 1}
	}
	return l
}

func startTerminalUI(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, auto *autoLiveDiff, store *mekugiReplayStore, activity *subagentActivity) (func() error, error) {
	width, height, err := term.GetSize(int(stdout.Fd()))
	if err != nil {
		return nil, err
	}
	// StartWithSize sets the controlling terminal; inherited stdio must not bypass it.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(1, width)), Rows: uint16(max(1, height-1))})
	if err != nil {
		return nil, err
	}
	// creack/pty's setup ioctl returns the master to blocking mode. Restore
	// nonblocking I/O so Close can interrupt readers even if a descendant keeps
	// the slave open. Later resizes must not call File.Fd and undo this.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = master.Close()
		return nil, err
	}
	auto.enable()
	u := &terminalUI{master: master, codex: vt.NewEmulator(max(1, width), max(1, height-1)), diffScreen: vt.NewEmulator(1, 3),
		auto: auto, activity: activity, agents: newLiveActivityView(), width: width, height: height, cursorVisible: true}
	u.configureCodex(stdout)
	u.diff = newLiveDiffTerminalController(store, "", stdout)
	u.diff.stdout = u.diffScreen
	u.diff.size = func() (int, int, error) { return max(1, u.layout.diff.w), max(3, u.layout.diff.h), nil }
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return func() error {
		defer master.Close()
		defer u.diff.close()
		defer u.diffScreen.Close()
		defer auto.enabled.Store(false)
		defer activity.releasePane()
		// Emulator replies (cursor queries, mode queries, etc.) go only to Codex.
		replies := make(chan struct{})
		replyPipe := u.codex.InputPipe().(io.Closer)
		go func() { defer close(replies); _, _ = io.Copy(master, u.codex); _ = replyPipe.Close() }()
		// Close the synchronized pipe first, join Read, then mutate VT's plain
		// closed field. VT.Close concurrent with VT.Read is not race-safe.
		defer func() { _ = replyPipe.Close(); <-replies; _ = u.codex.Close() }()
		uiCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		output := make(chan []byte, 16)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			defer close(output)
			buf := make([]byte, 32768)
			for {
				n, e := master.Read(buf)
				if n > 0 {
					select {
					case output <- append([]byte(nil), buf[:n]...):
					case <-uiCtx.Done():
						return
					}
				}
				if e != nil {
					return
				}
			}
		}()
		defer func() { cancel(); master.Close(); <-readDone }()
		var processErr error
		exited := false
		err := withRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
			return u.run(uiCtx, stdout, keys, output, done, &exited, &processErr)
		})
		if !exited {
			_ = cmd.Process.Kill()
			processErr = <-done
		}
		if err != nil {
			return err
		}
		// Preserve Codex's exit summary (or last screen after an abnormal exit)
		// outside our alternate screen, including sessions that finish before a tick.
		for _, line := range u.codex.Scrollback().Lines() {
			if _, err := fmt.Fprintln(stdout, line.Render()); err != nil {
				return err
			}
		}
		if final := strings.TrimRight(u.codex.Render(), "\n"); strings.TrimSpace(ansi.Strip(final)) != "" {
			if _, err := fmt.Fprintln(stdout, final); err != nil {
				return err
			}
		}
		return processErr
	}, nil
}

func (u *terminalUI) run(ctx context.Context, stdout *os.File, keys <-chan byte, output <-chan []byte, done <-chan error, exited *bool, processErr *error) error {
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	frames := time.NewTicker(33 * time.Millisecond)
	defer frames.Stop()
	sub := u.auto.events.subscribe()
	defer func() { u.auto.events.mu.Lock(); delete(u.auto.events.subs, sub); u.auto.events.mu.Unlock() }()
	dirty := true
	agePaint := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			*exited = true
			*processErr = err
			done = nil
			// Drain PTY output before restoring the screen.
			if output == nil {
				return nil
			}
		case data, ok := <-output:
			if !ok {
				output = nil
				if *exited {
					return nil
				}
				continue
			}
			before := u.codex.ScrollbackLen()
			if _, err := u.codex.Write(data); err != nil {
				return err
			}
			if u.hostError != nil {
				return u.hostError
			}
			if u.codexScroll > 0 {
				u.codexScroll += u.codex.ScrollbackLen() - before
			}
			dirty = true
		case <-resizes:
			var err error
			u.width, u.height, err = term.GetSize(int(stdout.Fd()))
			if err != nil {
				return err
			}
			dirty = true
		case key, ok := <-keys:
			if !ok {
				return io.EOF
			}
			if err := u.key(key); err != nil {
				return err
			}
			dirty = true
		case <-u.activity.pane.wake:
			dirty = true
		case <-u.auto.changed:
			u.auto.mu.Lock()
			u.side = u.side || u.auto.requested
			u.activityOpen = u.activityOpen || u.auto.activityRequested
			u.diff.workspace = u.auto.workspace
			u.auto.mu.Unlock()
			u.side = u.side || u.activityOpen
			dirty = true
		case <-sub.gap:
			u.diff.previewPane = liveDiffPreviewPane{}
			sub = u.auto.events.subscribe()
			dirty = true
		case <-sub.previewReady:
			for _, event := range u.auto.events.takePreviews(sub) {
				u.applyDiff(ctx, event)
			}
			dirty = true
		case event := <-sub.events:
			u.applyDiff(ctx, event)
			dirty = true
		case <-u.diff.previewFrameC:
			u.diff.previewFrameC = nil
			u.diff.previewFrameDue = time.Time{}
			u.diff.dirty = true
			dirty = true
		case <-u.diff.escapeC:
			u.diff.escapeC = nil
			u.diff.escape = ""
			u.diff.navigation.filtering, u.diff.navigation.focused, u.diff.help = false, false, false
			dirty = true
		case <-frames.C:
			if u.sequence == "\x1b" && time.Since(u.sequenceAt) >= 40*time.Millisecond {
				u.sequence = ""
				if err := u.send("\x1b"); err != nil {
					return err
				}
				dirty = true
			}
			if u.activityOpen && time.Since(agePaint) >= time.Second {
				dirty = true
				agePaint = time.Now()
			}
			if u.activityOpen && u.generation == 0 {
				generation, snapshot, ok := u.activity.subscribePane()
				if ok {
					u.generation = generation
					u.agents.apply(snapshot)
					dirty = true
				}
			}
			var pending activityPaneEvent
			if u.generation != 0 && dirty {
				entries, agents, ok := u.activity.takePane(u.generation)
				if ok && (len(entries) > 0 || !slices.Equal(agents, u.agents.agents)) {
					pending = activityPaneEvent{Kind: "entries", Entries: entries, Agents: agents}
					u.agents.apply(pending)
					dirty = true
				}
			}
			if dirty {
				if err := u.paint(ctx, stdout); err != nil {
					u.activity.restorePane(pending.Entries)
					return err
				}

				dirty = false
			}
		}
	}
}

func (u *terminalUI) paint(ctx context.Context, out io.Writer) error {
	l := terminalGeometry(u.width, u.height, u.split, u.horizontal, u.focus, u.side, u.activityOpen)
	if l.codex.w > 0 && (l.codex.w != u.codex.Width() || l.codex.h != u.codex.Height()) {
		u.codex.Resize(l.codex.w, l.codex.h)
		if err := resizeTerminalPTY(u.master, l.codex.w, l.codex.h); err != nil {
			return err
		}
	}
	u.layout = l
	if l.diff.w > 0 {
		if u.diffScreen.Width() != l.diff.w || u.diffScreen.Height() != l.diff.h {
			u.diffScreen.Resize(l.diff.w, l.diff.h)
			u.diff.dirty = true
		}
		if u.diffFailure == "" {
			if err := u.diff.renderFrame(ctx); err != nil {
				u.diffFailure = err.Error()
			}
		}
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l\x1b[0m")
	for row := 0; row < max(1, u.height); row++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", row+1)
	}
	draw := func(r terminalRect, lines []string) {
		for row := 0; row < r.h && row < len(lines); row++ {
			fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[0m%s\x1b[0m", r.y+row+1, r.x+1, ansi.Truncate(lines[row], r.w, ""))
		}
	}
	if l.codex.w > 0 {
		draw(l.codex, strings.Split(u.codexFrame(), "\n"))
	}
	if l.diff.w > 0 {
		if u.diffFailure != "" {
			draw(l.diff, []string{"Diff unavailable", livediff.Safe(u.diffFailure, false), "Use mchanges to review captured edits."})
		} else {
			draw(l.diff, strings.Split(u.diffScreen.Render(), "\n"))
		}
	}
	if l.agents.w > 0 {
		draw(l.agents, u.agents.render(l.agents.w, l.agents.h, time.Now()))
	}
	if l.vertical >= 0 {
		for row := 0; row < u.height-1; row++ {
			fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[2m│\x1b[0m", row+1, l.vertical+1)
		}
	}
	if l.horizontal >= 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[2m%s\x1b[0m", l.horizontal+1, l.diff.x+1, strings.Repeat("─", l.diff.w))
	}
	title := []string{"CODEX", "DIFF", "AGENTS"}[u.focus]
	status := " " + title + " · Ctrl-B 1/2/3 focus · ←/→ width · ↑/↓ height · [/] files · drag borders"
	if u.prefix {
		status = " Layout: 1/2/3 focus · arrows resize · [/] files · PgUp/PgDn Codex history · Ctrl-B sends prefix"
	}
	fmt.Fprintf(&b, "\x1b[%d;1H\x1b[7m%s\x1b[0m", max(1, u.height), ansi.Truncate(status, max(1, u.width), ""))
	if u.focus == 0 && l.codex.w > 0 && u.cursorVisible && u.codexScroll == 0 {
		p := u.codex.CursorPosition()
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", l.codex.y+p.Y+1, l.codex.x+p.X+1)
	}
	b.WriteString("\x1b[?2026l")
	_, err := io.WriteString(out, b.String())
	return err
}

// Decode once before routing. In particular, mouse reports and bracketed paste
// must never leak their payload into pane commands or layout shortcuts.
func (u *terminalUI) key(key byte) error {
	// Paste payload is opaque, including incomplete OSC/CSI and shortcut bytes.
	// Forward it immediately; only the exact end marker changes parser state.
	if u.paste {
		const end = "\x1b[201~"
		if key == end[u.pasteEnd] {
			u.pasteEnd++
		} else if key == 27 {
			u.pasteEnd = 1
		} else {
			u.pasteEnd = 0
		}
		if u.pasteEnd == len(end) {
			u.paste = false
			u.pasteEnd = 0
		}
		if u.focus == 0 {
			_, err := u.master.Write([]byte{key})
			return err
		}
		return nil
	}

	// Terminal OSC replies belong to Codex even when an auxiliary view has focus.
	// Never interpret clipboard payload bytes as navigation or flush shortcuts.
	if u.hostReply != nil {
		done := key == 7 || u.hostReplyEscape && key == '\\'
		u.hostReplyEscape = key == 27
		if len(u.hostReply) < 4<<20 && !u.hostReplyDiscard {
			u.hostReply = append(u.hostReply, key)
		} else {
			u.hostReplyDiscard = true
		}
		if !done {
			return nil
		}
		reply, discard := u.hostReply, u.hostReplyDiscard
		u.hostReply = nil
		u.hostReplyDiscard = false
		u.hostReplyEscape = false
		if !discard {
			_, err := u.master.Write(reply)
			return err
		}
		return nil
	}
	if u.sequence == "\x1b" && key == ']' {
		u.sequence = ""
		u.hostReply = []byte{27, ']'}
		return nil
	}

	if u.sequence != "" {
		u.sequence += string(key)
		if len(u.sequence) > 128 {
			u.sequence = ""
			return nil
		}
		s := u.sequence
		if s == "\x1b[" || s == "\x1bO" {
			return nil
		}
		if len(s) > 2 && s[1] == '[' && (key < 0x40 || key > 0x7e) {
			return nil
		}
		u.sequence = ""
		if s == "\x1b[200~" {
			u.paste = true
			u.pasteEnd = 0
			u.prefix = false
			if u.focus == 0 {
				u.codexScroll = 0
				_, err := io.WriteString(u.master, s)
				return err
			}
			return nil
		}
		if s == "\x1b[201~" {
			u.paste = false
			if u.focus == 0 {
				_, err := io.WriteString(u.master, s)
				return err
			}
			return nil
		}
		if strings.HasPrefix(s, "\x1b[<") {
			return u.mouse(s)
		}
		if u.prefix {
			u.prefix = false
			u.resize(s)
			return nil
		}
		return u.send(s)
	}
	if key == 27 {
		u.sequence = "\x1b"
		u.sequenceAt = time.Now()
		return nil
	}
	if u.prefix {
		u.prefix = false
		switch key {
		case 2:
			_, err := u.master.Write([]byte{2})
			return err
		case '1':
			u.focus = 0
		case '2':
			u.focus = 1
			u.side = true
		case '3':
			u.focus = 2
			u.side = true
			u.activityOpen = true
		default:
			u.resize(string(key))
		}
		return nil
	}
	if key == 2 {
		u.prefix = true
		return nil
	}
	return u.send(string(key))
}

func (u *terminalUI) resize(key string) {
	if key == "\x1b[5~" || key == "\x1b[6~" {
		delta := u.codex.Height()
		if key == "\x1b[6~" {
			delta = -delta
		}
		u.scrollCodex(delta)
		return
	}
	if u.split == 0 {
		u.split = max(30, u.width/2)
	}
	if u.horizontal == 0 {
		u.horizontal = max(4, (u.height-1)*3/5)
	}
	switch key {
	case "\x1b[D", "h":
		u.split -= 3
	case "\x1b[C", "l":
		u.split += 3
	case "\x1b[A", "k":
		u.horizontal--
	case "\x1b[B", "j":
		u.horizontal++
	case "[", "]":
		n := &u.diff.navigation
		if n.columns == 0 {
			n.columns = max(25, u.layout.diff.w/4)
		}
		if key == "[" {
			n.columns -= 2
		} else {
			n.columns += 2
		}
		n.columns = max(16, n.columns)
		u.diff.dirty = true
	}
	u.split = min(max(30, u.split), max(30, u.width-41))
	u.horizontal = min(max(4, u.horizontal), max(4, u.height-6))
}

func (u *terminalUI) send(s string) error {
	if u.focus == 0 {
		u.codexScroll = 0
		_, err := io.WriteString(u.master, s)
		return err
	}
	for _, key := range []byte(s) {
		if u.focus == 1 {
			if u.diff.handleKey(key) {
				u.focus = 0
			}
		} else {
			var quit bool
			u.agentEscape, quit = u.agents.handleKey(u.agentEscape, key)
			if quit {
				u.focus = 0
			}
		}
	}
	return nil
}

func (u *terminalUI) mouse(s string) error {
	fields := strings.Split(s[3:len(s)-1], ";")
	if len(fields) != 3 {
		return nil
	}
	var v [3]int
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil
		}
		v[i] = n
	}
	button, x, y := v[0], v[1]-1, v[2]-1
	release := s[len(s)-1] == 'm'
	if release && u.drag != 0 {
		u.drag = 0
		return nil
	}
	if button&32 != 0 && u.drag != 0 {
		switch u.drag {
		case 1:
			u.split = x
		case 2:
			u.horizontal = y
		case 3:
			u.diff.navigation.columns = max(16, x-u.layout.diff.x)
			u.diff.dirty = true
		}
		return nil
	}
	if button&^28 == 0 && !release {
		switch {
		case x == u.layout.vertical && u.layout.vertical >= 0:
			u.drag = 1
			return nil
		case y == u.layout.horizontal && x > u.layout.vertical && u.layout.horizontal >= 0:
			u.drag = 2
			return nil
		case u.layout.diff.contains(x, y) && u.diff.diffMode && u.diff.navigation.width(u.layout.diff.w) > 0 && x-u.layout.diff.x == u.diff.navigation.width(u.layout.diff.w):
			u.drag = 3
			return nil
		}
	}
	pane := -1
	var r terminalRect
	switch {
	case u.layout.codex.contains(x, y):
		pane = 0
		r = u.layout.codex
	case u.layout.diff.contains(x, y):
		pane = 1
		r = u.layout.diff
	case u.layout.agents.contains(x, y):
		pane = 2
		r = u.layout.agents
	}
	if pane < 0 {
		return nil
	}
	if button&^28 == 0 && !release {
		u.focus = pane
	}
	translated := fmt.Sprintf("\x1b[<%d;%d;%dM", button, x-r.x+1, y-r.y+1)
	if pane == 0 {
		if !release && button&64 != 0 && len(u.mouseModes) == 0 && !u.codex.IsAltScreen() {
			delta := 1
			if button&1 != 0 {
				delta = -1
			}
			u.scrollCodex(delta)
			return nil
		}
		m := uv.Mouse{X: x - r.x, Y: y - r.y}
		if button&4 != 0 {
			m.Mod |= uv.ModShift
		}
		if button&8 != 0 {
			m.Mod |= uv.ModAlt
		}
		if button&16 != 0 {
			m.Mod |= uv.ModCtrl
		}
		switch button &^ 60 {
		case 0:
			m.Button = uv.MouseLeft
		case 1:
			m.Button = uv.MouseMiddle
		case 2:
			m.Button = uv.MouseRight
		case 64:
			m.Button = uv.MouseWheelUp
		case 65:
			m.Button = uv.MouseWheelDown
		}
		switch {
		case release:
			u.codex.SendMouse(uv.MouseReleaseEvent(m))
		case button&64 != 0:
			u.codex.SendMouse(uv.MouseWheelEvent(m))
		case button&32 != 0:
			u.codex.SendMouse(uv.MouseMotionEvent(m))
		default:
			u.codex.SendMouse(uv.MouseClickEvent(m))
		}
		return nil
	}
	if release {
		return nil
	}
	if pane == 1 {
		for _, key := range []byte(translated) {
			u.diff.handleKey(key)
		}
		return nil
	}
	action := byte(0)
	switch button &^ 28 {
	case 0:
		action = '\r'
	case 35:
		action = 'h'
	case 64:
		action = 'k'
	case 65:
		action = 'j'
	}
	u.agents.handleMouse(action, y-r.y+1, x-r.x+1)
	return nil
}

func (u *terminalUI) applyDiff(ctx context.Context, event liveDiffEvent) {
	if u.diffFailure != "" {
		return
	}
	if _, err := u.diff.applyEvent(ctx, event); err != nil {
		u.diffFailure = err.Error()
	}
}
