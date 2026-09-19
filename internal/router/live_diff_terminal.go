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
	dirty             bool
	followDirty       bool

	files  []liveDiffFile
	lines  []string
	offset int
	rows   int
	theme  liveDiffTheme
	mouse  liveDiffMouse
	osc    liveDiffOSC
	escape string
}

func newLiveDiffTerminalController(store *mekugiReplayStore, workspace string, stdout *os.File) *liveDiffTerminalController {
	previewFrame := time.NewTimer(time.Hour)
	previewFrame.Stop()
	theme := liveDiffEnvironmentTheme(os.Getenv("COLORFGBG"))
	return &liveDiffTerminalController{
		store: store, workspace: workspace, stdout: stdout,
		data: newLiveDiffData(), coverage: "CONNECTING",
		view:              liveDiffView{scroll: make(map[string]int), following: true},
		previewFrame:      previewFrame,
		renderedFocusFile: -1, dirty: true, followDirty: true,
		theme: theme, renderedTheme: theme,
	}
}

func (c *liveDiffTerminalController) close() {
	c.previewFrame.Stop()
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
	files := make([]liveDiffFile, len(c.view.files))
	focusFile := -1
	for i, file := range c.view.files {
		files[i] = c.view.visible[file.key()]
		if slices.ContainsFunc(file.chunks, func(chunk liveDiffChunk) bool { return chunk.key == c.view.latest }) {
			focusFile = i
		}
	}
	focus := c.view.latestChunk()
	focus.snapshotOrder = 0 // Snapshot numbering does not change a capture's geometry.
	sameFiles := reflect.DeepEqual(c.rendered, files)
	if !sameFiles || c.renderedFocus != focus || c.renderedFocusFile != focusFile || width != c.lastWidth || c.theme != c.renderedTheme {
		previous := c.rendering
		c.rendering, err = c.renderer.render(ctx, c.theme, files, c.workspace, width, focusFile, focus)
		if err != nil {
			return err
		}
		if width != c.lastWidth && sameFiles {
			c.view.reflow(previous, c.rendering)
		}
		c.renderedTheme = c.theme
		c.rendered, c.renderedFocus, c.renderedFocusFile = files, focus, focusFile
		c.dirty, c.followDirty = true, true
	}
	if height != c.lastHeight {
		c.dirty, c.followDirty = true, true
	}
	c.lastWidth, c.lastHeight = width, height
	lines := c.rendering.lines
	rows := height - 2
	offset := 0
	if len(c.view.files) > 0 {
		start := c.rendering.starts[c.view.selected]
		end := len(lines)
		if c.view.selected+1 < len(c.view.files) {
			end = c.rendering.starts[c.view.selected+1]
		}
		offset = start + min(c.view.scroll[c.view.files[c.view.selected].key()], max(0, end-start-1))
	}
	if c.view.following && c.followDirty {
		offset = c.rendering.followOffset(rows)
	}
	c.followDirty = false
	// A flushed/reverted last file has an empty span at EOF. Normalize the
	// actual viewport offset too, not only scrollTo's selection argument.
	offset = max(0, min(offset, len(lines)-1))
	c.view.scrollTo(c.rendering, offset)
	c.files, c.lines, c.offset, c.rows = files, lines, offset, rows
	if !c.dirty {
		return nil
	}

	var active liveDiffFile
	if len(lines) > 0 {
		active = files[c.view.selected]
	}
	header := "Waiting for captured workspace edits..."
	if len(c.view.files) > 0 {
		header = "No unreviewed changes"
	}
	if len(lines) > 0 {
		label := liveDiffDisplayPath(c.workspace, active.path)
		end := len(lines)
		if c.view.selected+1 < len(files) {
			end = c.rendering.starts[c.view.selected+1]
		}
		start := c.rendering.starts[c.view.selected]
		number, total := 0, 0
		for i, file := range files {
			if len(file.chunks) > 0 {
				total++
				if i <= c.view.selected {
					number++
				}
			}
		}
		header = fmt.Sprintf("%d/%d  %s  | row %d/%d", number, total, label, offset-start+1, end-start)
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
	if c.diffMode && len(lines) > 0 {
		header = liveDiffGutter(active.highlighted, c.theme) + liveDiffHeader(header, width-3, c.rendering.counts[c.view.selected], c.theme)
	} else {
		header = liveDiffGutter(false, c.theme) + liveDiffSafe(header, false)
	}
	writeRow(1, header)
	if c.diffMode {
		for row := range rows {
			text := ""
			if index := offset + row; index < len(lines) {
				text = lines[index]
			}
			writeRow(row+2, text)
		}
	}
	if !c.diffMode {
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
	mode := "FOLLOW"
	if !c.view.following {
		mode = "PAUSED"
		if c.view.unseenUpdate {
			mode += " · new changes available"
		}
	}
	if c.coverage != "" && !strings.HasPrefix(c.coverage, "SIMULATION:") {
		mode, _, _ = strings.Cut(c.coverage, ":")
	}
	if c.diffMode {
		writeRow(height, "DIFF · v stream · "+mode+" · r follow · j/k · n/p · f/F · q quit")
	} else {
		writeRow(height, "STREAM · v diff · q quit")
	}
	screen.WriteString("\x1b[?2026l")
	if _, err := io.WriteString(c.stdout, screen.String()); err != nil {
		return err
	}
	c.dirty = false
	return nil
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
			c.followDirty = c.diffMode && c.view.following
		}
	case "coverage":
		c.coverage, c.dirty = event.Status, true
		if strings.HasPrefix(c.coverage, "RECONNECTING:") {
			c.previewPane = liveDiffPreviewPane{}
		}
	case "preview":
		if event.Preview.Workspace == "" || c.scope.Workspaces[event.Preview.Workspace][event.Preview.Thread] {
			c.previewPane.update(*event.Preview)
			if c.previewFrameC == nil {
				c.previewFrame.Reset(liveDiffPreviewFrameDelay)
				c.previewFrameC = c.previewFrame.C
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
		c.view.merge(c.data.files())
		c.view.refreshVisible()
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
	if c.osc.active || c.escape == "\x1b" && key == ']' {
		c.escape = ""
		if reply, complete := c.osc.consume(key); complete {
			if detected, ok := liveDiffBackgroundTheme(reply); ok {
				c.theme = detected
			}
		}
		return false
	}
	// Decode common terminal keys incrementally, including fragmented reads.
	if key == 27 {
		c.mouse = liveDiffMouse{}
		c.escape = "\x1b"
		return false
	}
	if c.mouse.active || c.escape == "\x1b[" && key == '<' {
		c.escape = ""
		key, _ = c.mouse.consume(key)
		if key == 0 || !c.diffMode {
			return false
		}
	}
	if c.escape != "" {
		c.escape += string(key)
		switch c.escape {
		case "\x1b[", "\x1b[5", "\x1b[6", "\x1bO":
			return false
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		case "\x1b[C", "\x1bOC", "\x1b[D", "\x1bOD":
			c.escape = ""
			return false
		case "\x1b[5~":
			key = 'b'
		case "\x1b[6~":
			key = ' '
		}
		c.escape = ""
	}
	if key != 'q' && key != 'v' && !c.diffMode {
		c.dirty = true
		return false
	}
	if strings.ContainsRune("np\tjk bgG", rune(key)) {
		c.view.following = false
	}
	switch key {
	case 'q':
		return true
	case 'v':
		c.diffMode = !c.diffMode
		c.followDirty = c.diffMode && c.view.following
	case 'r':
		c.view.followLatest()
		c.followDirty = true
	case 'f', 'F':
		c.view.flush(key == 'F')
	case 'n', '\t':
		for range len(c.view.files) {
			c.view.selected = (c.view.selected + 1) % len(c.view.files)
			if len(c.files[c.view.selected].chunks) > 0 {
				break
			}
		}
	case 'p':
		for range len(c.view.files) {
			c.view.selected = (c.view.selected + len(c.view.files) - 1) % len(c.view.files)
			if len(c.files[c.view.selected].chunks) > 0 {
				break
			}
		}
	case 'j':
		c.view.scrollTo(c.rendering, c.offset+1)
	case 'k':
		c.view.scrollTo(c.rendering, c.offset-1)
	case ' ':
		c.view.scrollTo(c.rendering, c.offset+c.rows)
	case 'b':
		c.view.scrollTo(c.rendering, c.offset-c.rows)
	case 'g':
		c.view.scrollTo(c.rendering, 0)
	case 'G':
		c.view.scrollTo(c.rendering, max(0, len(c.lines)-c.rows))
	}
	c.dirty = true
	return false
}
