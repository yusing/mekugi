package router

import (
	"cmp"
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
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"golang.org/x/term"
)

type liveDiffTerminalController struct {
	store     *mekugiReplayStore
	workspace string
	stdout    io.Writer
	size      func() (int, int, error)

	data *liveDiffData
	// callerCounts totals captured edit lines by caller key for the roster.
	callerCounts map[string]livediff.Counts
	// netCounts is the unfiltered composed project outcome, not caller activity.
	netCounts   *livediff.Counts
	scope       liveDiffScope
	coverage    string
	view        livediff.View
	previewPane diffview.PreviewPane

	previewFrame    *time.Timer
	previewFrameC   <-chan time.Time
	previewFrameDue time.Time
	turnRevision    uint64
	diffMode        bool
	renderer        livediff.Renderer
	rendering       livediff.Render
	rendered        []livediff.File
	renderedTheme   livediff.Theme
	lastWidth       int
	lastHeight      int
	diffWidth       int
	navigation      diffview.Navigation
	navigationFile  string
	help            bool
	escapeTimer     *time.Timer
	escapeC         <-chan time.Time
	dirty           bool
	// pinned reports a frame without a title row, where a mid-file viewport
	// pins its file heading and scrolling reaches one row further.
	pinned bool
	// native panes hold only the saved diff. The shell docks the live stream
	// beside its caller and draws this pane's title bar and key hints, so the
	// frame has neither a heading nor a footer row, and a completed turn never
	// switches what the pane shows.
	native bool
	// awaitingResync ignores the previous subscription's queued events after
	// the view switched root threads, until the broker's snapshot arrives.
	awaitingResync bool

	files  []livediff.File
	lines  []string
	offset int
	rows   int
	// navRows is the file navigator's viewport: the diff rows, or the stacked
	// list's rows in a narrow native pane.
	navRows int
	stack   int
	// painted attributes each painted pane row to its source (zero for chrome);
	// empty when the last frame showed no selectable diff. The diff source
	// starts at pane column sourceX and row sourceY.
	painted          []livediff.LineSource
	sourceX, sourceY int
	// back undoes the last list action on Esc.
	back  liveDiffBack
	theme livediff.Theme
	// A reported background replaces the theme's assumed fade canvas.
	background   livediff.RGB
	backgrounded bool
	mouse        terminalui.Mouse
	osc          livediff.OSC
	escape       string
}

func newLiveDiffTerminalController(store *mekugiReplayStore, workspace string, stdout *os.File) *liveDiffTerminalController {
	previewFrame := time.NewTimer(time.Hour)
	previewFrame.Stop()
	theme := livediff.EnvironmentTheme(os.Getenv("COLORFGBG"))
	return &liveDiffTerminalController{
		store: store, workspace: workspace, stdout: stdout,
		size: func() (int, int, error) { return term.GetSize(int(stdout.Fd())) },
		data: newLiveDiffData(), coverage: "CONNECTING",
		view:         livediff.View{Scroll: make(map[string]int)},
		previewFrame: previewFrame, dirty: true,
		theme: theme, renderedTheme: theme,
	}
}

func (c *liveDiffTerminalController) close() {
	c.previewFrame.Stop()
	if c.escapeTimer != nil {
		c.escapeTimer.Stop()
	}
}

func (c *liveDiffTerminalController) renderFrame(ctx context.Context) error {
	width, height, err := c.size()
	if err != nil {
		return err
	}
	width, height = max(1, width), max(3, height)
	files := make([]livediff.File, len(c.view.Files))
	for i, file := range c.view.Files {
		files[i] = c.view.Visible[file.Key()]
	}
	navWidth := c.navigation.Width(width)
	if inline := width < 100; inline != c.navigation.Changes.Inline {
		c.navigation.Changes.Inline = inline
		c.navigation.Changes.Rebuild(&c.view, c.workspace)
	}
	navigatorVisible := c.diffMode && (navWidth > 0 || c.navigation.Focused && !c.navigation.Hidden)
	diffWidth := width
	if navWidth > 0 {
		diffWidth -= navWidth + 1
	}
	sameFiles := reflect.DeepEqual(c.rendered, files)
	if !sameFiles || diffWidth != c.diffWidth || c.theme != c.renderedTheme {
		previous := c.rendering
		c.renderer.Caller = diffview.CallerStyle(c.theme)
		c.renderer.LayoutOnly = true
		c.rendering, err = c.renderer.Render(ctx, c.theme, files, c.workspace, diffWidth)
		if err != nil {
			return err
		}
		if diffWidth != c.diffWidth && sameFiles {
			c.view.Reflow(previous, c.rendering)
		}
		c.renderedTheme = c.theme
		c.rendered = files
		c.dirty = true
	}
	resized := height != c.lastHeight || width != c.lastWidth
	if resized {
		c.dirty = true
	}
	c.lastWidth, c.lastHeight, c.diffWidth = width, height, diffWidth
	lines := c.rendering.Lines
	rows := height - 2
	if navigatorVisible {
		rows++ // The file navigator owns the first row; no separate title row.
	}
	titled := !navigatorVisible && !c.native
	// A native pane too narrow for the side navigator stacks the file or
	// change list above the diff instead of covering it; focus gives the list
	// more room.
	stack := 0
	if c.native {
		rows = height
		n := &c.navigation
		if c.diffMode && navWidth == 0 && !n.Hidden && height >= 12 && (len(files) > 1 || n.Focused || n.ChangesTab) {
			// Entries rebuild later in the frame; a tree adds about one folder per file.
			entries := max(len(n.Entries), 2*len(files))
			if n.ChangesTab {
				entries = len(n.Changes.Rows)
			}
			limit := min(12, height/3)
			if n.Focused {
				limit = height / 2
			}
			stack = min(max(entries, 1)+2, limit)
			rows = height - stack - 1
		}
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

	// A reverted last file has an empty span at EOF. Normalize the
	// actual viewport offset too, not only scrollTo's selection argument.
	offset = max(0, min(offset, len(lines)-1))
	if err := c.rendering.PaintViewport(ctx, offset, offset+rows); err != nil {
		return err
	}
	c.view.ScrollTo(c.rendering, offset)
	activeKey := ""
	if len(files) > 0 {
		activeKey = files[c.view.Selected].Key()
	}
	selectionChanged := c.navigationFile != activeKey
	c.navigationFile = activeKey
	c.files, c.lines, c.offset, c.rows, c.stack = files, lines, offset, rows, stack
	c.navRows = cmp.Or(stack, rows)
	c.pinned = !titled
	if !sameFiles {
		c.navigation.Rebuild(files, c.workspace)
		c.refreshChanges()
	}
	if resized && c.navigation.Focused {
		c.navigation.EnsureVisible(c.navRows)
	}
	if selectionChanged && !c.navigation.Focused {
		c.revealFile()
	}
	if !c.dirty {
		return nil
	}

	var active livediff.File
	if len(lines) > 0 {
		active = files[c.view.Selected]
	}
	header := "Waiting for captured workspace edits..."
	if len(c.view.Files) > 0 {
		header = "No visible changes"
	}
	if len(lines) > 0 {
		header = "Changes"
	}
	if !c.diffMode {
		header = "Diff preview"
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
	if titled {
		writeRow(1, header)
	}
	c.painted = nil
	if c.diffMode {
		var nav []string
		renderNav := func(width, rows int) []string {
			n := &c.navigation
			if n.ChangesTab {
				return n.Changes.Render(n.Focused, n.Filtering, c.view.Caller, width, rows, c.theme)
			}
			return n.Render(files, c.rendering.Counts, c.view.Selected, width, rows, c.theme)
		}
		if navWidth > 0 {
			nav = renderNav(navWidth, rows)
		}
		overlay := c.navigation.Focused && navWidth == 0 && stack == 0
		if overlay {
			nav = renderNav(width-1, rows)
		}
		if stack > 0 {
			if c.navigation.ChangesTab {
				c.navigation.Changes.EnsureVisible(stack)
			} else {
				c.navigation.EnsureVisible(stack)
			}
			stacked := renderNav(width-1, stack)
			for row := range stack {
				writeRow(row+1, stacked[row])
			}
			writeRow(stack+1, livediff.Subtle+strings.Repeat("─", max(0, width-1))+"\x1b[0m")
		}
		// Without a title row, pin the open file's header while its content
		// scrolls, so a mid-file viewport still names its file.
		sticky := -1
		if !titled && len(c.view.Files) > 0 && offset > c.rendering.Starts[c.view.Selected] {
			sticky = c.rendering.Starts[c.view.Selected]
		}
		top := 0 // Pane rows above the diff source rows.
		switch {
		case titled:
			top = 1
		case stack > 0:
			top = stack + 1
		}
		if !overlay && !c.help && rows > 0 {
			c.painted, c.sourceX, c.sourceY = make([]livediff.LineSource, top+rows), 0, top
			if navWidth > 0 {
				c.sourceX = navWidth + 1
			}
		}
		for row := range rows {
			text := ""
			index := offset + row
			if sticky >= 0 {
				index-- // The pinned heading takes row 0; lines[offset] stays visible below it.
			}
			if row == 0 && sticky >= 0 {
				text = lines[sticky]
			} else if index < len(lines) {
				text = lines[index]
			} else if row == 0 && len(lines) == 0 && (navWidth > 0 || c.native) {
				text = header
			}
			if c.painted != nil {
				if row == 0 && sticky >= 0 && sticky < len(c.rendering.Sources) {
					c.painted[top+row] = c.rendering.Sources[sticky]
				} else if index < len(lines) && index < len(c.rendering.Sources) {
					c.painted[top+row] = c.rendering.Sources[index]
				}
			}
			if overlay {
				text = nav[row]
			} else if navWidth > 0 {
				left := nav[row]
				text = left + strings.Repeat(" ", max(0, navWidth-ansi.StringWidth(left))) + livediff.Subtle + "│\x1b[0m" + text
			}
			if c.help {
				help := slices.DeleteFunc([]string{"", "  Diff navigation", "", "  s       focus / hide files", "  Tab     files / changes by caller", "          Changes: Enter caller filters · Enter h/l expand / collapse change", "  /       filter paths, or changes by id, @caller, source · Ctrl-U clear", "  t       tree / flat list", "  ↑↓ j/k  move focused list / scroll diff", "  ←→ h/l  collapse / expand folder", "  Enter   focus diff or toggle folder", "  n/p     next / previous matching file", "  [ / ]   previous / next hunk", "  { / }   previous / next change", "  a / 0   next caller / all callers", "  PgUp/Dn page · Home/End first / last", "  v       stream / diff", "  Esc     close picker or help", "  ?       close help", "  Ctrl-C  quit"}, func(line string) bool {
					return c.native && strings.HasPrefix(line, "  v ")
				})
				text = ""
				if row < len(help) {
					text = livediff.Safe(help[row], false)
				}
			}
			switch {
			case titled:
				writeRow(row+2, text)
			case stack > 0:
				writeRow(row+stack+2, text)
			default:
				writeRow(row+1, text)
			}
		}
	}
	if !c.diffMode {
		c.previewPane.Motion.Enabled = true
		c.previewPane.Motion.Canvas = c.theme.Canvas()
		if c.backgrounded {
			c.previewPane.Motion.Canvas.Background = c.background
		}
		previewLines, err := c.previewPane.Render(ctx, c.workspace, c.theme, width, rows)
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
	// A fade keeps frames coming until its rows reach their final colors.
	now := time.Now()
	if !c.diffMode && c.previewPane.Animating(now) && (c.previewFrameC == nil || c.previewFrameDue.After(now.Add(diffview.PreviewFrameDelay))) {
		c.previewFrame.Stop()
		c.previewFrame.Reset(diffview.PreviewFrameDelay)
		c.previewFrameC = c.previewFrame.C
		c.previewFrameDue = now.Add(diffview.PreviewFrameDelay)
	}
	mode := ""
	if c.coverage != "" && !strings.HasPrefix(c.coverage, "SIMULATION:") {
		mode, _, _ = strings.Cut(c.coverage, ":")
	}
	if mode != "" {
		mode = " · " + mode
	}
	// Each mode reports what is waiting in the other one.
	switch {
	case c.native:
	case c.diffMode:
		stream := "v stream"
		if live := c.previewPane.Live(); live > 0 {
			stream += fmt.Sprintf(" (%d live)", live)
		}
		scope := ""
		if c.view.Caller != "" {
			name, _ := diffview.CallerStyle(c.theme)(c.view.Caller)
			scope = " · @" + livediff.Safe(name, false) + " (0 all)"
		}
		writeRow(height, "DIFF · "+stream+mode+scope+" · s files · Tab changes · ? help")
	default:
		diff := "v diff"
		if count := len(c.files); count > 0 {
			diff += fmt.Sprintf(" (%d files)", count)
		}
		writeRow(height, "STREAM · "+diff)
	}
	screen.WriteString("\x1b[?2026l")
	if _, err := io.WriteString(c.stdout, screen.String()); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// resetScope drops the saved-diff projection before the view follows another
// root thread. Display preferences survive; files, filters and scroll do not.
func (c *liveDiffTerminalController) resetScope() {
	c.data, c.scope, c.callerCounts, c.coverage = newLiveDiffData(), liveDiffScope{}, nil, "CONNECTING"
	c.netCounts = nil
	c.view = livediff.View{Scroll: make(map[string]int)}
	c.navigation = diffview.Navigation{Flat: c.navigation.Flat}
	c.previewPane, c.back = diffview.PreviewPane{}, liveDiffBack{}
	c.files, c.lines, c.offset, c.rendered = nil, nil, 0, nil
	c.awaitingResync, c.dirty = true, true
	c.refreshChanges()
}

func (c *liveDiffTerminalController) applyEvent(ctx context.Context, event liveDiffEvent) (bool, error) {
	if err := validateLiveDiffEvent(event); err != nil {
		return false, err
	}
	if c.awaitingResync {
		if event.Kind != "scope" || !event.Resync {
			return false, nil
		}
		c.awaitingResync = false
	}
	switch event.Kind {
	case "end":
		return true, nil
	case "heartbeat":
	case "turn":
		if event.TurnRevision > c.turnRevision && c.native {
			c.turnRevision = event.TurnRevision
		} else if event.TurnRevision > c.turnRevision {
			c.turnRevision = event.TurnRevision
			c.diffMode = event.Status == "completed"
			c.dirty = true
		}
	case "coverage":
		c.coverage, c.dirty = event.Status, true
		if strings.HasPrefix(c.coverage, "RECONNECTING:") {
			c.previewPane = diffview.PreviewPane{}
		}
	case "preview":
		if event.Preview.Workspace == "" || c.scope.Workspaces[event.Preview.Workspace][event.Preview.Thread] {
			c.previewPane.Update(*event.Preview)
			// The producer already paces complete target units. A second
			// debounce here merges adjacent statements back into one frame.
			// Keep the timer only for animation after the new snapshot renders.
			c.previewFrame.Stop()
			c.previewFrameC = nil
			c.previewFrameDue = time.Time{}
			c.dirty = true
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
		c.callerCounts = liveDiffCallerCounts(c.view.Files)
		c.netCounts = liveDiffNetCounts(&c.view)
		// The Changes tab holds view indexes; keys may arrive before a repaint.
		c.refreshChanges()
		c.dirty = true
	}
	return false, nil
}

// liveDiffNetCounts reuses the saved view's composition and workspace scope.
// A caller filter affects navigation only, never the overall outcome. Only a
// filtered view needs another projection; the normal case reuses its cache.
func liveDiffNetCounts(view *livediff.View) *livediff.Counts {
	all := *view
	if all.Caller != "" {
		all.Caller, all.Visible = "", nil
	}
	all.RefreshVisible()
	var total *livediff.Counts
	for _, file := range all.Files {
		if !slices.ContainsFunc(file.Chunks, liveDiffProjectCapture) {
			continue
		}
		if total == nil {
			total = new(livediff.Counts)
		}
		count := all.Visible[file.Key()].NetCounts()
		if count.Added < 0 || count.Removed < 0 {
			return new(livediff.Counts{Added: -1, Removed: -1})
		}
		total.Added += count.Added
		total.Removed += count.Removed
	}
	return total
}

func liveDiffProjectCapture(chunk livediff.Chunk) bool {
	review := chunk.Review
	return chunk.Workspace == "" || review.Origin != "" ||
		slices.ContainsFunc([]string{review.BeforePath, review.AfterPath}, func(path string) bool {
			return path != "" && execPathWithin(path, chunk.Workspace)
		})
}

// liveDiffCallerCounts totals each caller's captured edit lines, regardless of
// the caller filter. Incomplete captures contribute no counts, rather than
// hiding confirmed edits from the same caller behind an unknown total.
// Files outside the capturing workspace, such as rewritten scratch files, are
// not project edits and stay out of the totals.
func liveDiffCallerCounts(files []livediff.File) map[string]livediff.Counts {
	counts := make(map[string]livediff.Counts)
	for _, file := range files {
		for _, chunk := range file.Chunks {
			added, removed := chunk.Review.LineCounts()
			if added < 0 || removed < 0 {
				continue
			}
			if !liveDiffProjectCapture(chunk) {
				continue
			}
			key := livediff.CallerKey(chunk.Caller)
			count := counts[key]
			count.Added, count.Removed = count.Added+added, count.Removed+removed
			counts[key] = count
		}
	}
	return counts
}

// liveDiffBack is what Esc returns to after Enter in the navigator: the list
// after opening a file ('f'), or the previous caller filter after picking a
// branch ('b').
type liveDiffBack struct {
	kind   byte
	caller string
}

// escapeKey handles a lone Esc: it closes help or the filter, then undoes the
// last list action, then leaves the list.
func (c *liveDiffTerminalController) escapeKey() {
	c.escape = ""
	n := &c.navigation
	back := c.back
	c.back = liveDiffBack{}
	switch {
	case c.help || n.Filtering:
		c.back = back
		n.Filtering, n.Focused, c.help = false, false, false
	case back.kind == 'b' && n.Focused:
		c.filterCaller(back.caller)
	case back.kind == 'f' && !n.Focused:
		n.Hidden, n.Focused, c.view.Following = false, true, false
	default:
		n.Focused = false
	}
	c.dirty = true
}

// pointNav tracks the navigator row under the pointer.
func (c *liveDiffTerminalController) pointNav(row int, inNav bool, firstRow int) {
	n := &c.navigation
	count, top := len(n.Entries), n.Top
	if n.ChangesTab {
		count, top = len(n.Changes.Rows), n.Changes.Top
	}
	hover := 0
	if index := top + row - firstRow - 2; inNav && row >= firstRow+2 && index < count {
		hover = index + 1
	}
	before := [2]int{n.Hover, n.Changes.Hover}
	n.Hover, n.Changes.Hover = 0, 0
	if n.ChangesTab {
		n.Changes.Hover = hover
	} else {
		n.Hover = hover
	}
	c.dirty = c.dirty || before != [2]int{n.Hover, n.Changes.Hover}
}

// clearHover forgets the pointed row, reporting whether one was shown.
func (c *liveDiffTerminalController) clearHover() bool {
	shown := c.navigation.Hover != 0 || c.navigation.Changes.Hover != 0
	c.navigation.Hover, c.navigation.Changes.Hover = 0, 0
	return shown
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
		c.mouse = terminalui.Mouse{}
		c.escape = "\x1b"
		return false
	}
	if c.mouse.Active || c.escape == "\x1b[" && key == '<' {
		c.escape = ""
		action, row, column := c.mouse.Consume(key)
		if action == 0 || action == 'h' && !c.diffMode || c.help {
			return false
		}
		if !c.diffMode {
			c.scroll(terminalui.PaneWheelKey(action))
			c.dirty = true
			return false
		}
		navWidth := c.navigation.Width(c.lastWidth)
		inNav := navWidth > 0 && column <= navWidth || navWidth == 0 && (c.stack > 0 && row <= c.stack || c.stack == 0 && c.navigation.Focused)
		navigatorVisible := navWidth > 0 || c.navigation.Focused && !c.navigation.Hidden
		firstRow, lastRow := 2, c.lastHeight-1
		if navigatorVisible || c.native {
			firstRow = 1
		}
		if c.native {
			lastRow = c.lastHeight
		}
		if action == 'h' {
			c.pointNav(row, inNav && row >= firstRow && row <= lastRow, firstRow)
			return false
		}
		if row < firstRow || row > lastRow {
			return false
		}
		c.dirty = true
		if action == '\r' {
			c.navigation.Focused = inNav
			c.navigation.Filtering = false
		}
		if inNav && c.navigation.ChangesTab {
			l := &c.navigation.Changes
			if action == '\r' {
				if row == firstRow+1 {
					c.navigationKey('/')
					return false
				}
				if index := l.Top + row - firstRow - 2; row >= firstRow+2 && index < len(l.Rows) {
					l.Cursor, c.navigation.Focused, c.view.Following = index, true, false
					c.openChangeRow()
					c.navigation.Focused = true
				}
			} else {
				delta := 1
				if action == 'k' {
					delta = -1
				}
				l.Top = max(0, min(l.Top+delta, max(0, len(l.Rows)-max(1, c.navRows-2))))
			}
			return false
		}
		if inNav {
			n := &c.navigation
			if action == '\r' {
				if row == firstRow+1 {
					c.navigationKey('/')
					return false
				}
				index := n.Top + row - firstRow - 2
				if row >= firstRow+2 && index < len(n.Entries) {
					n.Cursor, n.Focused, c.view.Following = index, true, false
					c.openNavEntry()
					n.Focused = true
				}
			} else {
				c.view.Following = false
				delta := 1
				if action == 'k' {
					delta = -1
				}
				n.Top = max(0, min(n.Top+delta, max(0, len(n.Entries)-max(1, c.navRows-2))))
			}
			return false
		}
		if action != '\r' {
			c.scroll(terminalui.PaneWheelKey(action))
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
		c.navigation.Filtering = false
	}
	if key != 'v' && !c.diffMode {
		c.scroll(key)
		c.dirty = true
		return false
	}
	if c.diffMode {
		c.dirty = true
		if key == '?' && !c.navigation.Filtering {
			c.help = !c.help
			return false
		}
		if c.help {
			return false
		}
		if c.navigationKey(key) {
			return false
		}
	}
	if c.scroll(key) {
		return false
	}
	if strings.ContainsRune("npjk bgG[]{}", rune(key)) {
		c.view.Following = false
	}
	switch key {
	case 'v':
		if c.native {
			break
		}
		c.diffMode = !c.diffMode
	case '{', '}':
		if c.diffMode {
			c.stepChange(map[byte]int{'{': -1, '}': 1}[key])
		}
	case 'a':
		if c.diffMode {
			c.cycleCaller()
		}
	case '0':
		if c.diffMode {
			c.filterCaller("")
		}
	case 'n':
		c.stepFile(1)
	case 'p':
		c.stepFile(-1)
	case '[', ']':
		if key == ']' {
			for _, at := range c.rendering.Hunks {
				if at > c.offset {
					c.view.JumpTo(c.rendering, at)
					break
				}
			}
		} else {
			for _, at := range slices.Backward(c.rendering.Hunks) {
				if at < c.offset {
					c.view.JumpTo(c.rendering, at)
					break
				}
			}
		}

	}
	c.dirty = true
	return false
}

// callerDots marks each file with the callers of its shown captures, once
// more than one caller has changes.
func (c *liveDiffTerminalController) callerDots() []string {
	if len(diffview.Callers(&c.view)) < 2 {
		return nil
	}
	style := diffview.CallerStyle(c.theme)
	dots := make([]string, len(c.view.Files))
	for i, file := range c.view.Files {
		var seen []string
		for _, chunk := range file.Chunks {
			if !c.view.Shows(chunk) || slices.Contains(seen, chunk.Caller) {
				continue
			}
			seen = append(seen, chunk.Caller)
			_, color := style(chunk.Caller)
			dots[i] += color + "●\x1b[0m"
		}
		if dots[i] != "" {
			dots[i] = " " + dots[i]
		}
	}
	return dots
}

// nativeTitle summarizes the saved diff for the shell's title bar: file and
// line totals on the left; coverage, caller filter, focus and the open
// file's position on the right.
func (c *liveDiffTerminalController) nativeTitle() (string, string) {
	left := "no captured edits yet"
	if len(c.files) > 0 {
		var total livediff.Counts
		for _, file := range c.files {
			count := file.NetCounts()
			if count.Added < 0 || count.Removed < 0 {
				total = livediff.Counts{Added: -1, Removed: -1}
				break
			}
			total.Added += count.Added
			total.Removed += count.Removed
		}
		left = fmt.Sprintf("%d files", len(c.files))
		if len(c.files) == 1 {
			left = "1 file"
		}
		left += diffview.CountStats(total, c.theme)
	}
	var right []string
	if c.navigation.Focused {
		right = append(right, "files focus")
	} else {
		right = append(right, "diff focus")
	}
	if c.coverage != "" {
		status, _, _ := strings.Cut(c.coverage, ":")
		right = append(right, livediff.Safe(status, false))
	}
	if c.view.Caller != "" {
		name, _ := diffview.CallerStyle(c.theme)(c.view.Caller)
		right = append(right, "@"+livediff.Safe(name, false))
	}

	if len(c.files) > 1 {
		right = append(right, fmt.Sprintf("%d/%d", c.view.Selected+1, len(c.files)))
	}
	return left, strings.Join(right, activityui.Dim+" · "+activityui.Undim)
}
