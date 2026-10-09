package router

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type terminalSelection struct {
	rect                       terminalRect
	rows                       []string
	copySource                 [][]activityui.CopySpan
	contentLeft                []int // Optional per-row start after numbered gutters.
	startX, startY, endX, endY int
	dragging, moved            bool
	link                       string
	view                       *liveActivityView
	pane                       terminalRect
	questions                  []uint64
	snippets                   []liveActivitySnippet
	mention                    string                // Concise description Reference inserts.
	diff                       []livediff.LineSource // Diff row attribution, aligned with rows.
	workspace                  string                // Diff workspace for the mentioned path.
	top, fixed                 int                   // Frozen document viewport and pinned prefix rows.
	screenRows                 int                   // Frame height at the press, for resize invalidation.
}

// document retains off-screen rows in the same coordinate space as the press.
// The visible snapshot wins, including pinned headings and link presentation.
func (s *terminalSelection) document(lines []string, top, fixed int) {
	if len(lines) <= s.rect.h {
		return
	}
	s.screenRows = len(s.rows)
	// Diff can start near EOF with blank viewport rows below its last line.
	// Keep that offset instead of moving the snapshot over earlier source rows.
	top = max(0, top)
	lines = append(slices.Clone(lines), make([]string, max(0, top+s.rect.h-len(lines)))...)
	rows := make([]string, s.rect.y+len(lines))
	copySource := make([][]activityui.CopySpan, len(rows))
	for i, line := range lines {
		rows[s.rect.y+i], copySource[s.rect.y+i] = activityui.ExtractCopy(strings.Repeat(" ", s.rect.x) + line)
	}
	s.top, s.fixed = top, fixed
	for y := s.rect.y; y < s.rect.y+s.rect.h && y < len(s.rows); y++ {
		at := s.documentY(y)
		rows[at] = s.rows[y]
		if y < len(s.copySource) {
			copySource[at] = s.copySource[y]
		}
	}
	s.rows, s.copySource = rows, copySource
	s.startY, s.endY = s.documentY(s.startY), s.documentY(s.endY)
}

func (s *terminalSelection) restart(x, y int, link string) *terminalSelection {
	next := *s
	next.startX, next.endX = x, x
	next.startY, next.endY = s.documentY(y), s.documentY(y)
	next.dragging, next.moved, next.link = true, false, link
	return &next
}

func (s *terminalSelection) documentY(y int) int {
	if y < s.rect.y+s.fixed {
		return y
	}
	return y + s.top
}

func (s *terminalSelection) scroll(delta int) {
	if s.screenRows == 0 {
		return
	}
	s.top = max(0, min(s.top+delta, len(s.rows)-s.rect.y-s.rect.h))
}

// mouse shares wheel extension and edge-drag scrolling across panes and dialogs.
func (s *terminalSelection) mouse(button, x, y int, release bool) bool {
	if s.screenRows != 0 && !release && button&64 != 0 && (s.dragging || s.rect.contains(x, y)) {
		delta := outputDialogWheelRows
		if button&1 == 0 {
			delta = -delta
		}
		s.scroll(delta)
		if s.dragging {
			s.move(x, y, false)
		}
		return true
	}
	if s.dragging && (release || button&32 != 0 && button&3 == 0) {
		if !release {
			if y < s.rect.y+s.fixed {
				s.scroll(-1)
			} else if y >= s.rect.y+s.rect.h {
				s.scroll(1)
			}
		}
		s.move(x, y, release)
		return true
	}
	return false
}

func (s *terminalSelection) scrollKey(key string) bool {
	delta := 0
	switch key {
	case "\x1b[A":
		delta = -1
	case "\x1b[B":
		delta = 1
	case "\x1b[5~":
		delta = -max(1, s.rect.h-s.fixed-1)
	case "\x1b[6~":
		delta = max(1, s.rect.h-s.fixed-1)
	case "\x1b[H", "\x1b[1~":
		delta = -len(s.rows)
	case "\x1b[F", "\x1b[4~":
		delta = len(s.rows)
	default:
		return false
	}
	if s.screenRows == 0 {
		return false
	}
	x, y := s.endX, s.endY-s.top
	if s.endY < s.rect.y+s.fixed {
		y = s.endY
	}
	s.scroll(delta)
	if s.dragging {
		s.move(x, y, false)
	}
	return true
}

// selectionSpan is the text a row offers for selection within [left, right).
// Leading frames and gutters, each with the space after it, and trailing
// frames and padding are decoration. Indentation inside the text is kept, so
// Main, Activity and the composer select alike.
func selectionSpan(row string, left, right int) (int, int) {
	start, end := left, left
	leading, decorated := true, false
	column := 0
	for g := uniseg.NewGraphemes(ansi.Strip(row)); g.Next() && column < right; {
		next := column + g.Width()
		if column >= left {
			cell := g.Str()
			switch {
			case selectionDecoration(cell):
				if leading {
					start, decorated = next, true
				}
			case cell == " ":
				if leading && decorated {
					start, decorated = next, false
				}
			default:
				leading, decorated, end = false, false, next
			}
		}
		column = next
	}
	return start, max(start, min(end, right))
}

// selectionDecoration reports frame, gutter, rail and rule glyphs: box
// drawing and block elements.
func selectionDecoration(cell string) bool {
	r := []rune(cell)
	return len(r) == 1 && r[0] >= 0x2500 && r[0] <= 0x259f
}

// ordered returns the selection's first and last rows.
func (s *terminalSelection) ordered() (int, int, int, int) {
	ax, ay, bx, by := s.startX, s.startY, s.endX, s.endY
	if ay > by || ay == by && ax > bx {
		ax, ay, bx, by = bx, by, ax, ay
	}
	return ax, ay, bx, by
}

// bounds is the selected text of row y, as a half-open column range.
func (s *terminalSelection) bounds(y int) (int, int) {
	ax, ay, bx, by := s.ordered()
	if y < ay || y > by {
		return 0, 0
	}
	var left, right int
	annotated := false
	if y < len(s.copySource) {
		for _, span := range s.copySource[y] {
			if span.Column < s.rect.x || span.Column >= s.rect.x+s.rect.w {
				continue
			}
			if !annotated {
				left, right = span.Column, span.Column
				annotated = true
			}
			if !span.Rule && !span.Omit && span.Width > 0 {
				left = min(left, span.Column)
				right = max(right, span.Column+span.Width)
			}
		}
	}
	if annotated {
		right = min(right, s.rect.x+s.rect.w)
	} else if y < len(s.contentLeft) {
		// Dialog rows have explicit gutters; glyphs inside their content are
		// literal output, including box drawing and block characters.
		left = max(s.rect.x, s.contentLeft[y])
		right = min(s.rect.x+s.rect.w, ansi.StringWidth(strings.TrimRight(ansi.Strip(s.rows[y]), " ")))
	} else {
		left, right = selectionSpan(s.rows[y], s.rect.x, s.rect.x+s.rect.w)
	}
	if y == ay {
		left = max(left, ax)
	}
	if y == by {
		right = min(right, bx+1)
	}
	if right <= left {
		return 0, 0
	}
	// Include the entire grapheme when either endpoint lands on a wide cell.
	column := 0
	for g := uniseg.NewGraphemes(ansi.Strip(s.rows[y])); g.Next(); {
		next := column + g.Width()
		if column < left && left < next {
			left = column
		}
		if column < right && right < next {
			right = next
		}
		column = next
		if column >= right {
			break
		}
	}
	return left, right
}

// move follows a drag within its snapshot, including a release outside it.
func (s *terminalSelection) move(x, y int, release bool) {
	s.endX = min(max(x, s.rect.x), s.rect.x+s.rect.w-1)
	s.endY = s.documentY(min(max(y, s.rect.y), s.rect.y+s.rect.h-1))
	s.moved = s.moved || s.endX != s.startX || s.endY != s.startY
	if release {
		s.dragging = false
	}
}

// row paints the selection without retaining highlight escapes in its snapshot.
func (s *terminalSelection) row(y int, theme livediff.Theme) string {
	row := s.rows[y]
	if left, right := s.bounds(y); s.moved && right > left {
		return ansi.Cut(row, 0, left) + activityui.TextSelection(ansi.Cut(row, left, right), theme) + ansi.Cut(row, right, ansi.StringWidth(row))
	}
	return row
}

// selectionKey owns scrolling and action dispatch for both selection surfaces.
// Callers retain navigation dismissal and their surrounding Escape priority.
func (u *terminalUI) selectionKey(key string) bool {
	s := u.selection
	if s == nil {
		return false
	}
	if s.scrollKey(key) {
		if d := u.output; d != nil {
			d.top, d.follow = s.top, false
		}
		return true
	}
	if u.output != nil && key == "y" {
		key = "c"
	}
	var action byte
	switch key {
	case "\x1b":
		action = 27
	case "r", "R":
		action = 'r'
	case "c", "C", "\x03":
		action = 'c'
	default:
		return false
	}
	if s.dragging && action != 27 && u.output == nil {
		return false
	}
	u.selectionAction(action)
	return true
}

func (u *terminalUI) selectionAction(action byte) {
	s := u.selection
	if s == nil || s.dragging && action != 27 {
		return
	}
	switch action {
	case 'r':
		editor := u.main.promptEditor()
		if editor.currentQuestion() != nil {
			// A question answer is plain text; it cannot carry a mention's quote.
			editor.run = runNone
			editor.insertDraft("> " + s.text() + "\n\n")
		} else {
			description, source := s.mentionDescription()
			editor.insertSelection(description, s.text(), source)
		}
		editor.refreshPicker()
		editor.run = runNone
		u.focus = 0
		u.output = nil
	case 'c':
		u.copyText(s.text())
	case 27:
	default:
		return
	}
	u.selection = nil
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	previous := u.selection
	d := u.output
	body := terminalRect{}
	footer := terminalRect{0, u.height - 1, u.width, 1}
	mouseX, mouseY := x, y
	if d != nil {
		body = terminalRect{d.rect.x + 2, d.rect.y + d.chrome() - 1, d.rect.w - 4, d.rows}
		mouseX, mouseY = x-body.x, y-body.y
		footer = terminalRect{d.rect.x, d.rect.y + d.rect.h - 1, d.rect.w, 1}
	}
	if s := previous; s != nil {
		if s.mouse(button, mouseX, mouseY, release) {
			if d != nil {
				d.top = s.top
				if s.moved {
					d.follow = false
				}
			}
			if release {
				if !s.moved {
					u.selection = nil
					if s.link != "" {
						view := s.view
						if d != nil {
							view = d.view
						}
						if !u.openMarkdownFile(view, s.link) && d == nil {
							link := s.link
							if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
								link = parsed.Path
							}
							u.copyText(link)
						}
					} else if d == nil {
						index := s.startY - s.rect.y
						if index >= 0 && index < len(s.questions) && s.questions[index] != 0 {
							if s.view == u.main.view && u.openActivityReply(s.questions[index]) {
								return true
							}
							u.openEntry(s.view, s.questions[index])
						} else if index >= 0 && index < len(s.snippets) && s.snippets[index] != (liveActivitySnippet{}) {
							snippet := s.snippets[index]
							if snippet.block == editNavigationSnippet {
								u.openActivityEdit(s.view, snippet.run, snippet.path)
							} else if !u.openOutput(s.view, snippet) {
								u.openEntry(s.view, snippet.run)
							}
						}
					}
				}
			}
			return true
		}
		if !s.dragging && button == 0 && !release && footer.contains(x, y) {
			at, capacity := x, u.width
			if d != nil {
				at, capacity = x-d.rect.x-3, d.rect.w-6
				if ansi.StringWidth(selectionHints.render()) > capacity {
					capacity-- // The dialog renderer reserves its last cell for an ellipsis.
				}
			}
			u.selectionAction(selectionHints.actionAt(at, capacity))
			return true
		}
		dismiss := button&64 != 0 || button == 0 && !release
		if d != nil {
			pressed := button &^ 28
			dismiss = !release && (pressed == 0 || pressed == 64 || pressed == 65)
		}
		if dismiss {
			u.selection = nil
		}
	}
	if d != nil {
		if button&^28 != 0 || release || !body.contains(x, y) || len(d.body) == 0 {
			return false
		}
		s := &terminalSelection{rect: terminalRect{0, 0, body.w, len(d.body)}, rows: d.body, contentLeft: d.indents, startX: mouseX, startY: mouseY, endX: mouseX, endY: mouseY, dragging: true, mention: "dialog"}
		_, s.link = u.selectionScreen(d.body, mouseX, mouseY)
		var lines []string
		var indents []int
		for i := range d.laid.Lines {
			for _, row := range d.laid.Rows(i, body.w) {
				lines = append(lines, d.highlight(row, d.laid.Indent(i), i == d.match))
				indents = append(indents, d.laid.Indent(i))
			}
		}
		s.document(lines, d.top, 0)
		if s.screenRows != 0 {
			s.contentLeft = indents
		}
		u.selection = s
		return true
	}
	if button != 0 || release || u.drag != 0 || u.main == nil || u.main.keybindings {
		return false
	}
	var view *liveActivityView
	var pane terminalRect
	mention := "message"
	switch {
	case u.layout.codex.contains(x, y):
		view, pane = u.main.view, u.layout.codex
	case u.layout.agents.contains(x, y):
		view, pane, mention = u.agents, u.layout.agents, "activity"
	case u.layout.diff.contains(x, y):
		// The press still reaches the diff pane, so clicks keep their meaning.
		if previous != nil && previous.diff != nil && previous.screenRows != 0 && previous.pane == u.layout.diff && previous.rect.contains(x, y) {
			u.selection = previous.restart(x, y, "")
		} else {
			u.selection = u.diffSelection(x, y)
		}
		return false
	default:
		return false
	}
	rect := terminalRect{pane.x + view.feedLeft - 1, pane.y + view.feedTop - 1, view.feedRight - view.feedLeft + 1, view.feedRows}
	rect.w = min(rect.w, pane.x+pane.w-rect.x)
	rect.h = min(rect.h, pane.y+pane.h-rect.y)
	questions, snippets := view.feedQuestions, view.feedSnippets
	if view == u.main.view && u.main.btw != nil {
		btw := u.main.btw.rect
		btw.x += pane.x
		btw.y += pane.y
		if btw.contains(x, y) {
			rect, mention, questions, snippets = btw, "side answer", nil, nil
		}
	}
	if view == u.main.view && !rect.contains(x, y) {
		composer := u.main.composerRect
		composer.x += pane.x
		composer.y += pane.y
		if composer.contains(x, y) {
			rect, mention = composer, "text"
			questions, snippets = nil, nil
		}
	}
	if view == u.main.view && u.main.statusPanel != nil {
		rect, mention = u.main.statusPanel.rect, "status"
		rect.x += pane.x
		rect.y += pane.y
		questions, snippets = nil, nil
	}
	if !rect.contains(x, y) || len(u.paintedRows) != u.height {
		return false
	}
	source := u.paintedRows
	copySource := u.paintedCopy
	if previous != nil && previous.view == view && previous.rect.contains(x, y) {
		rect = previous.rect
		source, copySource = slices.Clone(source), slices.Clone(copySource)
		for row := rect.y; row < rect.y+rect.h; row++ {
			at := previous.documentY(row)
			source[row] = previous.rows[at]
			if row < len(copySource) && at < len(previous.copySource) {
				copySource[row] = previous.copySource[at]
			}
		}
		questions, snippets, mention = previous.questions, previous.snippets, previous.mention
	}
	rows, link := u.selectionScreen(source, x, y)
	if view == u.main.view {
		u.focus = 0
	} else {
		u.focus = 2
	}
	u.selection = &terminalSelection{rect: rect, rows: rows, copySource: copySource, startX: x, startY: y, endX: x, endY: y, dragging: true, link: link, view: view, pane: pane, questions: questions, snippets: snippets, mention: mention}
	if previous != nil && previous.screenRows != 0 && previous.view == view && previous.rect == rect {
		u.selection = previous.restart(x, y, link)
	} else {
		u.selectionDocument(u.selection)
	}
	return true
}

func (u *terminalUI) selectionDocument(s *terminalSelection) {
	switch s.mention {
	case "message", "activity":
		if s.view.feedLines > s.rect.h {
			feed := s.view.renderFeed(s.rect.w, s.view.feedLines)
			top, fixed := s.view.offset, 0
			if !s.view.conversation && top+1 < len(feed.heads) && feed.heads[top] != top && feed.heads[top+1] == feed.heads[top] {
				feed.lines = append([]string{feed.lines[feed.heads[top]]}, feed.lines...)
				feed.questions = append([]uint64{0}, feed.questions...)
				feed.snippets = append([]liveActivitySnippet{{}}, feed.snippets...)
				top, fixed = top+1, 1
			}
			s.document(feed.lines, top, fixed)
			questions, snippets := s.questions, s.snippets
			s.questions = append(slices.Clone(feed.questions), make([]uint64, max(0, len(s.rows)-s.rect.y-len(feed.questions)))...)
			s.snippets = append(slices.Clone(feed.snippets), make([]liveActivitySnippet, max(0, len(s.rows)-s.rect.y-len(feed.snippets)))...)
			for row := 0; row < s.rect.h; row++ {
				at := s.documentY(s.rect.y+row) - s.rect.y
				if row < len(questions) {
					s.questions[at] = questions[row]
				}
				if row < len(snippets) {
					s.snippets[at] = snippets[row]
				}
			}
		}
	case "side answer":
		b := u.main.btw
		var lines []string
		fixed := s.rect.h - min(b.rows, len(b.copyAnswer))
		for y := s.rect.y; y < s.rect.y+fixed; y++ {
			lines = append(lines, ansi.Cut(s.rows[y], s.rect.x, s.rect.x+s.rect.w))
		}
		lines = append(lines, b.copyAnswer...)
		s.document(lines, max(0, len(b.copyAnswer)-b.rows-b.scroll), fixed)
	}
}

// selectionScreen snapshots painted rows as a terminal shows them, with the
// hyperlink under the pointer.
func (u *terminalUI) selectionScreen(source []string, x, y int) ([]string, string) {
	screen := vt.NewEmulator(u.width, u.height)
	defer screen.Close()
	for row, line := range source {
		fmt.Fprintf(screen, "\x1b[%d;1H%s", row+1, line)
	}
	link := ""
	if cell := screen.CellAt(x, y); cell != nil {
		link = cell.Link.URL
	}
	return strings.Split(screen.Render(), "\n"), link
}

// diffSelection starts a selection right of the file navigator and its
// divider; each row's text starts after its gutter and line number.
func (u *terminalUI) diffSelection(x, y int) *terminalSelection {
	c, pane := u.diff, u.layout.diff
	if c == nil || !c.diffMode || u.diffFailure != "" || len(c.painted) <= c.sourceY || len(u.paintedRows) != u.height {
		return nil
	}
	// Rows above sourceY are the title or a stacked navigator and its rule.
	rect := terminalRect{pane.x + c.sourceX, pane.y + c.sourceY, pane.w - c.sourceX, min(pane.h, len(c.painted)) - c.sourceY}
	if !rect.contains(x, y) {
		return nil
	}
	rows, _ := u.selectionScreen(u.paintedRows, x, y)
	contentLeft := make([]int, rect.y+rect.h)
	sources := make([]livediff.LineSource, rect.y+rect.h)
	for row, source := range c.painted[c.sourceY : c.sourceY+rect.h] {
		contentLeft[rect.y+row] = rect.x + source.Content
		sources[rect.y+row] = source
	}
	s := &terminalSelection{rect: rect, rows: rows, contentLeft: contentLeft, startX: x, startY: y, endX: x, endY: y, dragging: true, pane: pane, diff: sources, workspace: c.workspace}
	if len(c.lines) > rect.h {
		lines, sources := c.lines, c.rendering.Sources
		fixed := 0
		if c.pinned && len(c.view.Files) > c.view.Selected && c.offset > c.rendering.Starts[c.view.Selected] {
			heading := c.rendering.Starts[c.view.Selected]
			lines = append([]string{lines[heading]}, lines...)
			sources = append([]livediff.LineSource{sources[heading]}, sources...)
			fixed = 1
		}
		s.document(lines, c.offset, fixed)
		s.contentLeft = make([]int, len(s.rows))
		s.diff = make([]livediff.LineSource, len(s.rows))
		for i, source := range sources {
			if rect.y+i >= len(s.rows) {
				break
			}
			s.contentLeft[rect.y+i] = rect.x + source.Content
			s.diff[rect.y+i] = source
		}
	}
	return s
}

// mentionDescription names the selection concisely: its pane, or for the
// saved diff, the change IDs and gutter lines the selected rows show.
func (s *terminalSelection) mentionDescription() (string, string) {
	if s.diff == nil {
		return s.mention, ""
	}
	_, first, _, last := s.ordered()
	var changes, paths []string
	// Gutter lines of changed rows, used only for one change. New-file
	// coordinates win; a deletion-only selection names old-file lines.
	low, high, oldLow, oldHigh := 0, 0, 0, 0
	for y := first; y <= last && y < len(s.diff); y++ {
		if left, right := s.bounds(y); right <= left {
			continue
		}
		source := s.diff[y]
		if source.Path != "" && !slices.Contains(paths, source.Path) {
			paths = append(paths, source.Path)
		}
		if source.Change == "" {
			continue
		}
		if !slices.Contains(changes, source.Change) {
			changes = append(changes, source.Change)
		}
		switch {
		case source.Line > 0 && source.Deleted:
			oldLow, oldHigh = cmp.Or(min(oldLow, source.Line), source.Line), max(oldHigh, source.Line)
		case source.Line > 0:
			low, high = cmp.Or(min(low, source.Line), source.Line), max(high, source.Line)
		}
	}
	if high == 0 {
		low, high = oldLow, oldHigh
	}
	path := ""
	if len(paths) == 1 {
		path = pathdisplay.ForWorkspace(s.workspace, paths[0])
	}
	switch {
	case len(changes) == 0:
		return "diff", path
	case len(changes) > 1:
		return fmt.Sprintf("diff hunks @%s +%d", changes[0], len(changes)-1), path
	case high == 0:
		return "diff hunk @" + changes[0], path
	case low == high:
		return fmt.Sprintf("diff hunk @%s:%d", changes[0], low), path
	}
	return fmt.Sprintf("diff hunk @%s:%d-%d", changes[0], low, high), path
}

func (u *terminalUI) paintSelection(rows []string) {
	s := u.selection
	if s == nil || u.output != nil {
		return
	}
	if cmp.Or(s.screenRows, len(s.rows)) != len(rows) || u.paintedWidth != u.width || s.pane != u.layout.codex && s.pane != u.layout.agents && s.pane != u.layout.diff {
		u.selection = nil
		return
	}
	for y := s.rect.y; y < s.rect.y+s.rect.h; y++ {
		line := ansi.Cut(s.row(s.documentY(y), u.main.view.painter.Theme), s.rect.x, s.rect.x+s.rect.w)
		line += strings.Repeat(" ", max(0, s.rect.w-ansi.StringWidth(line)))
		rows[y] += fmt.Sprintf("\x1b[%dG\x1b[0m%s\x1b[0m", s.rect.x+1, line)
	}
	if !s.dragging {
		rows[len(rows)-1] = "\x1b[1G\x1b[0m" + ansi.Truncate(selectionHints.render(), u.width, "")
	}
}

// text uses the frozen visible annotations. Unannotated UI rows retain
// ordinary selection behavior, but never supply table contents or code fill.
func (s *terminalSelection) text() string {
	_, first, _, last := s.ordered()
	var parts []activityui.CopyFragment
	var out []string
	flush := func() {
		if len(parts) > 0 {
			out = append(out, activityui.CopyText(parts))
			parts = nil
		}
	}
	ax, ay, bx, by := s.ordered()
	for y := first; y <= last && y < len(s.rows); y++ {
		left, right := s.rect.x, s.rect.x+s.rect.w
		if y == ay {
			left = max(left, ax)
		}
		if y == by {
			right = min(right, bx+1)
		}
		annotated := false
		if y < len(s.copySource) {
			for _, span := range s.copySource[y] {
				// Ignore metadata belonging to another pane in this composed screen row.
				if span.Column < s.rect.x || span.Column >= s.rect.x+s.rect.w {
					continue
				}
				annotated = true
				if f, ok := span.Clip(left, right); ok {
					parts = append(parts, f)
				}
			}
		}
		if annotated {
			continue
		}
		flush()
		line := ""
		if l, r := s.bounds(y); r > l {
			line = strings.TrimRight(ansi.Strip(ansi.Cut(s.rows[y], l, r)), " ")
		}
		if line != "" || len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, line)
		}
	}
	flush()
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
