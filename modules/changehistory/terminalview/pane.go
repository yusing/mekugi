package terminalview

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Pane owns saved-review interaction, layout and source attribution.
// Source: apps/mekugi/internal/router/live_diff_terminal.go:27:695@[fc0514ff]
type Pane struct {
	publicationRevision              uint64
	workspace                        string
	coverage                         string
	failure                          string
	view                             View
	renderer                         Renderer
	rendering                        Render
	rendered                         []presentation.File
	renderedTheme                    diffrender.Theme
	lastWidth, lastHeight, diffWidth int
	navigation                       Navigation
	navigationFile                   string
	help, dirty                      bool
	files                            []presentation.File
	lines                            []string
	offset, rows, navRows, stack     int
	selection                        terminalui.SelectionSource
	selectionHeading                 int
	back                             paneBack
	theme                            diffrender.Theme
}

func NewPane(workspace string, theme diffrender.Theme) *Pane {
	return &Pane{workspace: workspace, coverage: "CONNECTING", view: View{Scroll: make(map[string]int)}, dirty: true, theme: theme, renderedTheme: theme}
}

func (c *Pane) Workspace() string { return c.workspace }
func (c *Pane) SetWorkspace(workspace string) {
	if c.workspace == workspace {
		return
	}
	c.workspace, c.rendered, c.dirty = workspace, nil, true
	c.refreshChanges()
}
func (c *Pane) SetTheme(theme diffrender.Theme) {
	c.dirty = c.dirty || c.theme != theme
	c.theme = theme
}
func (c *Pane) NavigatorWidth(width int) int { return c.navigation.Width(width) }
func (c *Pane) NavigatorColumns() int        { return c.navigation.Columns }
func (c *Pane) SetNavigatorColumns(columns int) {
	c.dirty = c.dirty || c.navigation.Columns != columns
	c.navigation.Columns = columns
}
func (c *Pane) Back() bool { return c.failure == "" && c.back.kind != 0 }

// Invalidate repaints a replaced terminal surface even when content is unchanged.
func (c *Pane) Invalidate() { c.dirty = true }

// Update consumes retained presentation values without reading their source store.
func (c *Pane) Update(publication presentation.Publication, coverage string) {
	c.coverage = coverage
	c.view.Update(publication.ForDiff(c.workspace))
	c.refreshChanges()
	c.dirty = true
}

// Reset retires the selected review while retaining its tree/flat preference.
func (c *Pane) Reset() {
	c.publicationRevision, c.failure = 0, ""
	c.coverage = "CONNECTING"
	c.view = View{Scroll: make(map[string]int)}
	c.navigation = Navigation{Flat: c.navigation.Flat}
	c.back = paneBack{}
	c.files, c.lines, c.offset, c.rendered = nil, nil, 0, nil
	c.dirty = true
	c.refreshChanges()
}

// EditorPath returns the selected file only when the Files list accepts editing.
// The application owns the explicit current-file read and editor handoff.
func (c *Pane) EditorPath() string {
	n := &c.navigation
	if c.failure != "" || c.help || !n.Focused || n.Filtering || n.ChangesTab || n.Cursor < 0 || n.Cursor >= len(n.Entries) {
		return ""
	}
	if file := n.Entries[n.Cursor].File; file >= 0 && file < len(c.files) {
		return c.files[file].Path
	}
	return ""
}

// Paint writes the saved pane into its borrowed terminal surface.
func (c *Pane) Paint(ctx context.Context, out io.Writer, width, height int) error {
	var err error
	width, height = max(1, width), max(3, height)
	if c.failure != "" {
		lines := []string{"Diff unavailable", textsanitize.Safe(c.failure, false), "Use mchanges to review captured edits."}
		var frame strings.Builder
		for row := range height {
			text := ""
			if row < len(lines) {
				text = ansi.Truncate(lines[row], width, "")
			}
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[0m\x1b[2K%s", row+1, text)
		}
		if _, err := io.WriteString(out, frame.String()); err != nil {
			return err
		}
		c.selection = terminalui.SelectionSource{}
		return nil
	}
	files := make([]presentation.File, len(c.view.Files))
	for i, file := range c.view.Files {
		files[i] = c.view.Visible[file.Key()]
	}
	navWidth := c.navigation.Width(width)
	if inline := width < 100; inline != c.navigation.Changes.Inline {
		c.navigation.Changes.Inline = inline
		c.navigation.Changes.Rebuild(&c.view.View, c.workspace)
	}
	diffWidth := width
	if navWidth > 0 {
		diffWidth -= navWidth + 1
	}
	sameFiles := reflect.DeepEqual(c.rendered, files)
	if !sameFiles || diffWidth != c.diffWidth || c.theme != c.renderedTheme {
		previous := c.rendering
		c.renderer.Caller = CallerStyle(c.theme)
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
	rows := height
	stack := 0

	n := &c.navigation
	if navWidth == 0 && !n.Hidden && height >= 12 && (len(files) > 1 || n.Focused || n.ChangesTab) {
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
	navResized := stack != c.stack || resized && c.navigation.Focused
	c.files, c.lines, c.offset, c.rows, c.stack = files, lines, offset, rows, stack
	c.navRows = cmp.Or(stack, rows)
	if !sameFiles {
		c.navigation.Rebuild(files, c.workspace)
		c.refreshChanges()
	}
	if navResized {
		if c.navigation.ChangesTab {
			c.navigation.Changes.EnsureVisible(c.navRows)
		} else {
			c.navigation.EnsureVisible(c.navRows)
		}
	}
	if selectionChanged && !c.navigation.Focused {
		c.revealFile()
	}
	if !c.dirty {
		return nil
	}

	header := "Waiting for captured workspace edits..."
	if len(c.view.Files) > 0 {
		header = "No visible changes"
	}
	if len(lines) > 0 {
		header = "Changes"
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
	header = diffrender.Gutter(false, c.theme) + textsanitize.Safe(header, false)

	var source terminalui.SelectionSource

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
		stacked := renderNav(width-1, stack)
		for row := range stack {
			writeRow(row+1, stacked[row])
		}
		writeRow(stack+1, diffrender.Subtle+strings.Repeat("─", max(0, width-1))+"\x1b[0m")
	}
	// Without a title row, pin the open file's header while its content
	// scrolls, so a mid-file viewport still names its file.
	sticky := -1
	if len(c.view.Files) > 0 && offset > c.rendering.Starts[c.view.Selected] {
		sticky = c.rendering.Starts[c.view.Selected]
	}
	top := 0 // Pane rows above the diff source rows.
	if stack > 0 {
		top = stack + 1
	}
	if !overlay && !c.help && rows > 0 {
		source = terminalui.SelectionSource{Y: top, Width: width, Painted: make([]diffrender.LineSource, rows),
			Document: lines, Sources: c.rendering.Sources, Offset: offset, Workspace: c.workspace}
		if navWidth > 0 {
			source.X = navWidth + 1
			source.Width -= source.X
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
		} else if row == 0 && len(lines) == 0 {
			text = header
		}
		if source.Painted != nil {
			if row == 0 && sticky >= 0 && sticky < len(c.rendering.Sources) {
				source.Painted[row] = c.rendering.Sources[sticky]
			} else if index < len(lines) && index < len(c.rendering.Sources) {
				source.Painted[row] = c.rendering.Sources[index]
			}
		}
		if overlay {
			text = nav[row]
		} else if navWidth > 0 {
			left := nav[row]
			text = left + strings.Repeat(" ", max(0, navWidth-ansi.StringWidth(left))) + diffrender.Subtle + "│\x1b[0m" + text
		}
		if c.help {
			help := []string{"", "  Diff navigation", "", "  s       focus / hide files", "  Tab     files / changes by caller", "          Changes: Enter caller filters · Enter h/l expand / collapse change", "  /       filter paths, or changes by id, @caller, source · Ctrl-U clear", "  t       tree / flat list", "  ↑↓ j/k  move focused list / scroll diff", "  ←→ h/l  collapse / expand folder", "  Enter   focus diff or toggle folder", "  n/p     next / previous matching file", "  [ / ]   previous / next hunk", "  { / }   previous / next change", "  a / 0   next caller / all callers", "  PgUp/Dn page · Home/End first / last", "  Esc     close picker or help", "  ?       close help", "  Ctrl-C  quit"}
			text = ""
			if row < len(help) {
				text = textsanitize.Safe(help[row], false)
			}
		}
		switch {
		case stack > 0:
			writeRow(row+stack+2, text)
		default:
			writeRow(row+1, text)
		}
	}

	screen.WriteString("\x1b[?2026l")
	if _, err := io.WriteString(out, screen.String()); err != nil {
		return err
	}
	// Retain the displayed document and pin independently of queued navigation
	// or evidence updates. Selection copies these slices only after a source hit.
	c.selection, c.selectionHeading = source, sticky
	c.dirty = false
	return nil
}

// Title summarizes the saved diff for the shell's title bar: file and
// line totals on the left; coverage, caller filter, focus and the open
// file's position on the right.
func (c *Pane) Title() (string, string) {
	if c.failure != "" {
		return "saved · unavailable", ""
	}
	left := "no captured edits yet"
	visible, position := 0, 0
	for i, file := range c.files {
		if len(file.Chunks) > 0 {
			visible++
			if i == c.view.Selected {
				position = visible
			}
		}
	}
	if len(c.files) > 0 {
		var total diffmodel.Counts
		for _, file := range c.files {
			if len(file.Chunks) == 0 {
				continue
			}
			count := file.NetCounts()
			if count.Added < 0 || count.Removed < 0 {
				total = diffmodel.Counts{Added: -1, Removed: -1}
				break
			}
			total.Added += count.Added
			total.Removed += count.Removed
		}
		left = fmt.Sprintf("%d files", visible)
		if visible == 1 {
			left = "1 file"
		}
		left += diffrender.CountStats(total, c.theme)
	}
	var right []string
	if c.navigation.Focused {
		right = append(right, "files focus")
		if !c.navigation.ChangesTab && !c.navigation.Filtering {
			right = append(right, "e editor")
		}
	} else {
		right = append(right, "diff focus")
	}
	if c.coverage != "" {
		status, _, _ := strings.Cut(c.coverage, ":")
		right = append(right, textsanitize.Safe(status, false))
	}
	if c.view.Caller != "" {
		name, _ := CallerStyle(c.theme)(c.view.Caller)
		right = append(right, "@"+textsanitize.Safe(name, false))
	}

	if visible > 1 {
		right = append(right, fmt.Sprintf("%d/%d", position, visible))
	}
	return "saved · " + left, strings.Join(right, terminalui.Dim+" · "+terminalui.Undim)
}

// Hints describes the saved-review controls in their current navigation state.
func (c *Pane) Hints() terminalui.Hints {
	if c.failure != "" {
		return terminalui.Hints{{"q", "close", 0}}
	}
	hints := terminalui.Hints{{"s", "files", 0}, {"tab", "changes", 0}, {"[ ]", "hunks", 0}, {"a", "caller", 0}, {"?", "help", 0}}
	if c.Back() {
		hints = append(terminalui.Hints{{"esc", "back", 0}}, hints...)
	}
	return hints
}
