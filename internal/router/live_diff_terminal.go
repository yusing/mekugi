package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"golang.org/x/term"
)

type liveDiffTerminalController struct {
	store     *mekugiReplayStore
	workspace string
	stdout    *os.File

	data        *liveDiffData
	scope       liveDiffScope
	coverage    string
	view        liveDiffView
	previewPane liveDiffPreviewPane

	previewFrame      *time.Timer
	previewFrameC     <-chan time.Time
	previewFrameDue   time.Time
	turnRevision      uint64
	diffMode          bool
	renderer          liveDiffRenderer
	rendering         liveDiffRender
	rendered          []liveDiffFile
	renderedFocus     liveDiffChunk
	renderedFocusFile int
	renderedTheme     liveDiffTheme
	lastWidth         int
	lastHeight        int
	diffWidth         int
	navigation        liveDiffNavigation
	navigationFile    string
	help              bool
	escapeTimer       *time.Timer
	escapeC           <-chan time.Time
	dirty             bool
	followDirty       bool

	files  []liveDiffFile
	lines  []string
	offset int
	rows   int
	theme  liveDiffTheme
	// A reported background replaces the theme's assumed fade canvas.
	background   livediff.RGB
	backgrounded bool
	mouse        liveDiffMouse
	osc          liveDiffOSC
	escape       string
}

func newLiveDiffTerminalController(store *mekugiReplayStore, workspace string, stdout *os.File) *liveDiffTerminalController {
	previewFrame := time.NewTimer(time.Hour)
	previewFrame.Stop()
	theme := livediff.EnvironmentTheme(os.Getenv("COLORFGBG"))
	return &liveDiffTerminalController{
		store: store, workspace: workspace, stdout: stdout,
		data: newLiveDiffData(), coverage: "CONNECTING",
		view:              liveDiffView{Scroll: make(map[string]int), Following: true},
		previewFrame:      previewFrame,
		renderedFocusFile: -1, dirty: true, followDirty: true,
		theme: theme, renderedTheme: theme,
	}
}

func (c *liveDiffTerminalController) close() {
	c.previewFrame.Stop()
	if c.escapeTimer != nil {
		c.escapeTimer.Stop()
	}
}

func (c *liveDiffTerminalController) run(
	ctx context.Context,
	events <-chan liveDiffEvent,
	keys <-chan byte,
	resizes <-chan os.Signal,
) error {
	for {
		if err := c.renderFrame(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.previewFrameC:
			c.previewFrameC, c.dirty = nil, true
			c.previewFrameDue = time.Time{}
			c.previewPane.expire(time.Now())
		case event, open := <-events:
			if !open {
				return nil
			}
		drainEvents:
			for drained := 0; ; drained++ {
				done, err := c.applyEvent(ctx, event)
				if err != nil || done {
					return err
				}
				// Consume already queued snapshots before painting. Preview updates
				// replace each other; durable events retain their original order.
				if drained >= cap(events) {
					break drainEvents
				}
				select {
				case event, open = <-events:
					if !open {
						return nil
					}
				default:
					break drainEvents
				}
			}
		case <-c.escapeC:
			c.escapeC = nil
			if c.escape == "\x1b" {
				c.escape = ""
				c.navigation.filtering, c.navigation.focused, c.help = false, false, false
				c.dirty = true
			}
		case <-resizes:
			c.dirty = true
		case key, open := <-keys:
			if !open || c.handleKey(key) {
				return nil
			}
		}
	}
}

func (c *liveDiffTerminalController) renderFrame(ctx context.Context) error {
	width, height, err := term.GetSize(int(c.stdout.Fd()))
	if err != nil {
		return err
	}
	width, height = max(1, width), max(3, height)
	files := make([]liveDiffFile, len(c.view.Files))
	focusFile := -1
	for i, file := range c.view.Files {
		files[i] = c.view.Visible[file.Key()]
		if slices.ContainsFunc(file.Chunks, func(chunk liveDiffChunk) bool { return chunk.Key == c.view.Latest }) {
			focusFile = i
		}
	}
	focus := c.view.LatestChunk()
	focus.SnapshotOrder = 0 // Snapshot numbering does not change a capture's geometry.
	navWidth := c.navigation.width(width)
	navigatorVisible := c.diffMode && (navWidth > 0 || c.navigation.focused && !c.navigation.hidden)
	diffWidth := width
	if navWidth > 0 {
		diffWidth -= navWidth + 1
	}
	sameFiles := reflect.DeepEqual(c.rendered, files)
	if !sameFiles || c.renderedFocus != focus || c.renderedFocusFile != focusFile || diffWidth != c.diffWidth || c.theme != c.renderedTheme {
		previous := c.rendering
		c.rendering, err = c.renderer.Render(ctx, c.theme, files, c.workspace, diffWidth, focusFile, focus)
		if err != nil {
			return err
		}
		if diffWidth != c.diffWidth && sameFiles {
			c.view.Reflow(previous, c.rendering)
		}
		c.renderedTheme = c.theme
		c.rendered, c.renderedFocus, c.renderedFocusFile = files, focus, focusFile
		c.dirty, c.followDirty = true, true
	}
	resized := height != c.lastHeight || width != c.lastWidth
	if resized {
		c.dirty, c.followDirty = true, true
	}
	c.lastWidth, c.lastHeight, c.diffWidth = width, height, diffWidth
	lines := c.rendering.Lines
	rows := height - 2
	if navigatorVisible {
		rows++ // The file navigator owns the first row; no separate title row.
	}
	offset := 0
	if len(c.view.Files) > 0 {
		start := c.rendering.Starts[c.view.Selected]
		end := len(lines)
		if c.view.Selected+1 < len(c.view.Files) {
			end = c.rendering.Starts[c.view.Selected+1]
		}
		offset = start + min(c.view.Scroll[c.view.Files[c.view.Selected].Key()], max(0, end-start-1))
	}
	if c.view.Following && c.followDirty {
		offset = c.rendering.FollowOffset(rows)
	}
	c.followDirty = false
	// A flushed/reverted last file has an empty span at EOF. Normalize the
	// actual viewport offset too, not only scrollTo's selection argument.
	offset = max(0, min(offset, len(lines)-1))
	c.view.ScrollTo(c.rendering, offset)
	activeKey := ""
	if len(files) > 0 {
		activeKey = files[c.view.Selected].Key()
	}
	selectionChanged := c.navigationFile != activeKey
	c.navigationFile = activeKey
	c.files, c.lines, c.offset, c.rows = files, lines, offset, rows
	if !sameFiles {
		c.navigation.rebuild(files, c.workspace)
	}
	if resized && c.navigation.focused {
		c.navigation.ensureVisible(rows)
	}
	if selectionChanged && !c.navigation.focused {
		c.revealFile()
	}
	if !c.dirty {
		return nil
	}

	var active liveDiffFile
	if len(lines) > 0 {
		active = files[c.view.Selected]
	}
	header := "Waiting for captured workspace edits..."
	if len(c.view.Files) > 0 {
		header = "No unreviewed changes"
	}
	if len(lines) > 0 {
		header = "Changes"
	}
	if !c.diffMode {
		header = "Waiting for live input..."
		if len(c.previewPane.order) > 0 {
			header = "Live input"
		}
	}
	if c.coverage != "" {
		header = c.coverage
	}
	var screen strings.Builder
	// Terminals may paint between reads even when a frame is one write.
	// Synchronized output keeps row clearing and replacement together.
	screen.WriteString("\x1b[?2026h")
	writeRow := func(row int, text string) {
		fmt.Fprintf(&screen, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row, ansi.Truncate(text, max(0, width-1), ""))
	}
	if c.diffMode && len(lines) > 0 && !navigatorVisible {
		header = livediff.Gutter(active.Highlighted, c.theme) + livediff.Header(header, diffWidth-3, c.rendering.Counts[c.view.Selected], c.theme)
	} else {
		header = livediff.Gutter(false, c.theme) + livediff.Safe(header, false)
	}
	if !navigatorVisible {
		writeRow(1, header)
	}
	if c.diffMode {
		var nav []string
		if navWidth > 0 {
			nav = c.navigation.render(files, c.rendering.Counts, c.view.Selected, navWidth, rows, c.theme)
		}
		overlay := c.navigation.focused && navWidth == 0
		if overlay {
			nav = c.navigation.render(files, c.rendering.Counts, c.view.Selected, width-1, rows, c.theme)
		}
		for row := range rows {
			text := ""
			if index := offset + row; index < len(lines) {
				text = lines[index]
			} else if row == 0 && len(lines) == 0 && navWidth > 0 {
				text = header
			}
			if overlay {
				text = nav[row]
			} else if navWidth > 0 {
				left := nav[row]
				text = left + strings.Repeat(" ", max(0, navWidth-ansi.StringWidth(left))) + "\x1b[2m│\x1b[0m" + text
			}
			if c.help {
				help := []string{"", "  Diff navigation", "", "  s       show / hide files", "  /       search and filter paths · Ctrl-U clear", "  t       tree / flat list", "  ↑↓ j/k  move or scroll", "  ←→ h/l  collapse / expand folder", "  Enter   open file or toggle folder", "  n/p     next / previous matching file", "  [ / ]   previous / next hunk", "  PgUp/Dn page · Home/End first / last", "  r       resume following changes", "  f / F   flush current / all files", "  v       stream / diff", "  Esc     close picker or help", "  ?       close help · q quit"}
				text = ""
				if row < len(help) {
					text = livediff.Safe(help[row], false)
				}
			}
			if navigatorVisible {
				writeRow(row+1, text)
			} else {
				writeRow(row+2, text)
			}
		}
	}
	if !c.diffMode {
		c.previewPane.motion.enabled = true
		c.previewPane.motion.canvas = c.theme.Canvas()
		if c.backgrounded {
			c.previewPane.motion.canvas.Background = c.background
		}
		previewLines, err := c.previewPane.render(ctx, c.workspace, c.theme, width, rows)
		if err != nil {
			return err
		}
		for row := range rows {
			text := ""
			if row < len(previewLines) {
				text = previewLines[row]
			}
			writeRow(row+2, text)
		}
	}
	// A fade keeps frames coming; otherwise wake only for the next expiry.
	now := time.Now()
	delay := c.previewPane.nextExpiry(now)
	if !c.diffMode && c.previewPane.animating(now) && (delay == 0 || delay > liveDiffPreviewFrameDelay) {
		delay = liveDiffPreviewFrameDelay
	}
	if due := now.Add(delay); delay > 0 && (c.previewFrameC == nil || c.previewFrameDue.After(due)) {
		c.previewFrame.Stop()
		c.previewFrame.Reset(delay)
		c.previewFrameC = c.previewFrame.C
		c.previewFrameDue = due
	}
	mode := "FOLLOW"
	if !c.view.Following {
		mode = "PAUSED"
		if c.view.UnseenUpdate {
			mode += " · new changes available"
		}
	}
	if c.coverage != "" && !strings.HasPrefix(c.coverage, "SIMULATION:") {
		mode, _, _ = strings.Cut(c.coverage, ":")
	}
	// Each mode reports what is waiting in the other one.
	if c.diffMode {
		stream := "v stream"
		if live := c.previewPane.live(); live > 0 {
			stream += fmt.Sprintf(" (%d live)", live)
		}
		writeRow(height, "DIFF · "+stream+" · "+mode+" · s files · ? help · q quit")
	} else {
		diff := "v diff"
		if pending := c.unreviewedFiles(); pending > 0 {
			diff += fmt.Sprintf(" (%d unreviewed)", pending)
		}
		writeRow(height, "STREAM · "+diff+" · q quit")
	}
	screen.WriteString("\x1b[?2026l")
	if _, err := io.WriteString(c.stdout, screen.String()); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

func (c *liveDiffTerminalController) unreviewedFiles() int {
	count := 0
	for _, file := range c.files {
		if len(file.Chunks) > 0 {
			count++
		}
	}
	return count
}

func (c *liveDiffTerminalController) applyEvent(ctx context.Context, event liveDiffEvent) (bool, error) {
	if err := validateLiveDiffEvent(event); err != nil {
		return false, err
	}
	switch event.Kind {
	case "end":
		return true, nil
	case "heartbeat":
	case "turn":
		if event.TurnRevision > c.turnRevision {
			c.turnRevision = event.TurnRevision
			c.diffMode = event.Status == "completed"
			c.dirty = true
			c.followDirty = c.diffMode && c.view.Following
		}
	case "coverage":
		c.coverage, c.dirty = event.Status, true
		if strings.HasPrefix(c.coverage, "RECONNECTING:") {
			c.previewPane = liveDiffPreviewPane{}
		}
	case "preview":
		if event.Preview.Workspace == "" || c.scope.Workspaces[event.Preview.Workspace][event.Preview.Thread] {
			prior := c.previewPane.views[event.Preview.ID]
			c.previewPane.update(*event.Preview)
			// Show the first usable frame and terminal state immediately. The
			// pacing timer is only for intermediate input deltas.
			if prior == nil || event.Preview.Complete || event.Preview.Workspace == "" ||
				event.Preview.Status == "" && event.Preview.Input == "" && len(event.Preview.Files) == 0 {
				c.previewFrame.Stop()
				c.previewFrameC = nil
				c.previewFrameDue = time.Time{}
				c.dirty = true
			} else {
				// A hold-expiry wake may be seconds away. A fresh input snapshot
				// must not wait for it (nor for some unrelated redraw).
				if due := time.Now().Add(liveDiffPreviewFrameDelay); c.previewFrameC == nil || c.previewFrameDue.After(due) {
					c.previewFrame.Stop()
					c.previewFrame.Reset(liveDiffPreviewFrameDelay)
					c.previewFrameC = c.previewFrame.C
					c.previewFrameDue = due
				}
			}
		}
	case "scope":
		if event.Resync {
			next, err := c.store.liveDiffSnapshot(ctx, *event.Scope)
			if err != nil {
				return false, err
			}
			for key := range c.data.attempts {
				if _, exists := next.attempts[key]; !exists {
					return false, errors.New("change records were removed; restart the live view")
				}
			}
			c.data = next
		} else if err := c.data.reconcile(ctx, c.store, *event.Scope); err != nil {
			return false, err
		}
		c.scope, c.coverage = *event.Scope, event.Status
	case "change":
		for _, change := range event.Changes {
			if !c.scope.Workspaces[change.Workspace][change.Thread] {
				continue
			}
			if err := c.data.apply(ctx, c.store, change); err != nil {
				return false, err
			}
		}
	}
	if event.Kind == "scope" || event.Kind == "change" {
		c.view.Merge(c.data.files())
		c.view.RefreshVisible()
		c.dirty = true
	}
	return false, nil
}

func (c *liveDiffTerminalController) handleKey(key byte) bool {
	// Raw mode must leave cancellation usable even during a malformed
	// terminal reply. All other OSC bytes stay separate from commands.
	if key == 3 {
		return true
	}
	if c.osc.Active || c.escape == "\x1b" && key == ']' {
		c.escape = ""
		if reply, complete := c.osc.Consume(key); complete {
			if detected, ok := livediff.BackgroundTheme(reply); ok {
				c.theme = detected
				c.background, c.backgrounded = livediff.BackgroundColor(reply)
			}
		}
		return false
	}
	// Decode common terminal keys incrementally, including fragmented reads.
	if c.escapeTimer != nil {
		c.escapeTimer.Stop()
		c.escapeC = nil
	}
	if key == 27 {
		if c.escapeTimer == nil {
			c.escapeTimer = time.NewTimer(40 * time.Millisecond)
		} else {
			c.escapeTimer.Reset(40 * time.Millisecond)
		}
		c.escapeC = c.escapeTimer.C
		c.mouse = liveDiffMouse{}
		c.escape = "\x1b"
		return false
	}
	if c.mouse.active || c.escape == "\x1b[" && key == '<' {
		c.escape = ""
		action, row, column := c.mouse.consume(key)
		if action == 0 || !c.diffMode || c.help {
			return false
		}
		navWidth := c.navigation.width(c.lastWidth)
		inNav := navWidth > 0 && column <= navWidth || navWidth == 0 && c.navigation.focused
		navigatorVisible := navWidth > 0 || c.navigation.focused && !c.navigation.hidden
		firstRow := 2
		if navigatorVisible {
			firstRow = 1
		}
		if row < firstRow || row >= c.lastHeight {
			return false
		}
		c.dirty = true
		if inNav {
			n := &c.navigation
			if action == '\r' {
				if row == firstRow+1 {
					c.navigationKey('/')
					return false
				}
				index := n.top + row - firstRow - 2
				if row >= firstRow+2 && index < len(n.entries) {
					n.cursor, n.focused, c.view.Following = index, true, false
					c.openNavEntry()
				}
			} else {
				c.view.Following = false
				delta := 1
				if action == 'k' {
					delta = -1
				}
				n.top = max(0, min(n.top+delta, max(0, len(n.entries)-max(1, c.rows-2))))
			}
			return false
		}
		if action != '\r' {
			delta := 1
			if action == 'k' {
				delta = -1
			}
			c.view.Following = false
			c.view.ScrollTo(c.rendering, c.offset+delta)
		}
		return false
	}
	if c.escape != "" {
		c.escape += string(key)
		switch c.escape {
		case "\x1b[", "\x1b[1", "\x1b[4", "\x1b[5", "\x1b[6", "\x1bO":
			return false
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		case "\x1b[C", "\x1bOC":
			key = 'l'
		case "\x1b[D", "\x1bOD":
			key = 'h'
		case "\x1b[H", "\x1bOH", "\x1b[1~":
			key = 'g'
		case "\x1b[F", "\x1bOF", "\x1b[4~":
			key = 'G'
		case "\x1b[5~":
			key = 'b'
		case "\x1b[6~":
			key = ' '
		}
		c.escape = ""
		c.navigation.filtering = false
	}
	if key != 'q' && key != 'v' && !c.diffMode {
		c.dirty = true
		return false
	}
	if c.diffMode {
		c.dirty = true
		if key == '?' && !c.navigation.filtering {
			c.help = !c.help
			return false
		}
		if c.help && key != 'q' {
			return false
		}
		if c.navigationKey(key) {
			return false
		}
	}
	if strings.ContainsRune("npjk bgG[]", rune(key)) {
		c.view.Following = false
	}
	switch key {
	case 'q':
		return true
	case 'v':
		c.diffMode = !c.diffMode
		c.followDirty = c.diffMode && c.view.Following
	case 'r':
		c.navigation.focused, c.navigation.filtering = false, false
		c.view.FollowLatest()
		c.followDirty = true
	case 'f', 'F':
		c.view.Flush(key == 'F')
	case 'n':
		c.stepFile(1)
	case 'p':
		c.stepFile(-1)
	case '[', ']':
		if key == ']' {
			for _, at := range c.rendering.Hunks {
				if at > c.offset {
					c.view.ScrollTo(c.rendering, at)
					break
				}
			}
		} else {
			for _, at := range slices.Backward(c.rendering.Hunks) {
				if at < c.offset {
					c.view.ScrollTo(c.rendering, at)
					break
				}
			}
		}
	case 'j':
		c.view.ScrollTo(c.rendering, c.offset+1)
	case 'k':
		c.view.ScrollTo(c.rendering, c.offset-1)
	case ' ':
		c.view.ScrollTo(c.rendering, c.offset+c.rows)
	case 'b':
		c.view.ScrollTo(c.rendering, c.offset-c.rows)
	case 'g':
		c.view.ScrollTo(c.rendering, 0)
	case 'G':
		c.view.ScrollTo(c.rendering, max(0, len(c.lines)-c.rows))
	}
	c.dirty = true
	return false
}
