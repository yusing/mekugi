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
	s.endY = min(max(y, s.rect.y), s.rect.y+s.rect.h-1)
	s.moved = s.moved || s.endX != s.startX || s.endY != s.startY
	if release {
		s.dragging = false
	}
}

// row paints the selection without retaining highlight escapes in its snapshot.
func (s *terminalSelection) row(y int) string {
	row := s.rows[y]
	if left, right := s.bounds(y); s.moved && right > left {
		return ansi.Cut(row, 0, left) + "\x1b[7m" + ansi.Strip(ansi.Cut(row, left, right)) + "\x1b[27m" + ansi.Cut(row, right, ansi.StringWidth(row))
	}
	return row
}

func (u *terminalUI) selectionAction(action byte) {
	if u.selection == nil {
		return
	}
	switch action {
	case 'r':
		if u.main.currentQuestion() != nil {
			// A question answer is plain text; it cannot carry a mention's quote.
			u.main.run = runNone
			u.main.insertDraft("> " + u.selection.text() + "\n\n")
		} else {
			description, source := u.selection.mentionDescription()
			u.main.insertSelection(description, u.selection.text(), source)
		}
		u.main.refreshPicker()
		u.main.run = runNone
		u.focus = 0
	case 'c':
		u.copyText(u.selection.text())
	default:
		return
	}
	u.selection = nil
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	previous := u.selection
	if s := u.selection; s != nil {
		if s.dragging && (release || button&32 != 0 && button&3 == 0) {
			s.move(x, y, release)
			if release {
				s.dragging = false
				if !s.moved {
					u.selection = nil
					if s.link != "" {
						u.copyText(s.link)
					} else {
						index := s.startY - s.rect.y
						if index < len(s.questions) && s.questions[index] != 0 {
							if s.view == u.main.view && u.openActivityReply(s.questions[index]) {
								return true
							}
							u.openEntry(s.view, s.questions[index])
						} else if index < len(s.snippets) && s.snippets[index] != (liveActivitySnippet{}) {
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
		if !s.dragging && button == 0 && !release && y == u.height-1 {
			switch selectionHints.actionAt(x, u.width) {
			case 'r':
				u.selectionAction('r')
			case 'c':
				u.selectionAction('c')
			case 27:
				u.selection = nil
			}
			return true
		}
		if button&64 != 0 || button == 0 && !release {
			u.selection = nil
		}
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
		u.selection = u.diffSelection(x, y)
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
		source, rect = previous.rows, previous.rect
		copySource = previous.copySource
		questions, snippets, mention = previous.questions, previous.snippets, previous.mention
	}
	rows, link := u.selectionScreen(source, x, y)
	if view == u.main.view {
		u.focus = 0
	} else {
		u.focus = 2
	}
	u.selection = &terminalSelection{rect: rect, rows: rows, copySource: copySource, startX: x, startY: y, endX: x, endY: y, dragging: true, link: link, view: view, pane: pane, questions: questions, snippets: snippets, mention: mention}
	return true
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
	if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
		link = parsed.Path
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
	return &terminalSelection{rect: rect, rows: rows, contentLeft: contentLeft, startX: x, startY: y, endX: x, endY: y, dragging: true, pane: pane, diff: sources, workspace: c.workspace}
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
	if s == nil {
		return
	}
	if len(s.rows) != len(rows) || u.paintedWidth != u.width || s.pane != u.layout.codex && s.pane != u.layout.agents && s.pane != u.layout.diff {
		u.selection = nil
		return
	}
	for y := s.rect.y; y < s.rect.y+s.rect.h; y++ {
		line := ansi.Cut(s.row(y), s.rect.x, s.rect.x+s.rect.w)
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
