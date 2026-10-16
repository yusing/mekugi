package terminalsession

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Surface is a frozen painted document, including source-copy annotations and
// optional numbered gutters. It contains no feature or backend state.
type Surface struct {
	Rect        Rect
	Rows        []string
	CopySource  [][]terminalui.CopySpan
	ContentLeft []int
}

// Selection owns cursor, drag and viewport state over a frozen source snapshot.
// Source: apps/mekugi/internal/router/terminal_selection.go:22:273,685:734@70e7ce50 terminalSelection
type Selection struct {
	Surface
	startX, startY, endX, endY int
	dragging, moved            bool
	top, fixed, screenRows     int
}

func NewSelection(surface Surface, x, y int) *Selection {
	return &Selection{Surface: surface, startX: x, endX: x, startY: y, endY: y, dragging: true}
}

func (s *Selection) Dragging() bool  { return s.dragging }
func (s *Selection) Moved() bool     { return s.moved }
func (s *Selection) Top() int        { return s.top }
func (s *Selection) ScreenRows() int { return s.screenRows }
func (s *Selection) PressRow() int   { return s.startY }

type SelectionIntent byte

const (
	SelectionScroll    SelectionIntent = 1
	SelectionClear     SelectionIntent = 27
	SelectionReference SelectionIntent = 'r'
	SelectionCopy      SelectionIntent = 'c'
)

// Key applies selection-local navigation and exposes source actions for the
// application to bind to its editor, clipboard and dialog lifetime.
func (s *Selection) Key(key string, dialog bool) (SelectionIntent, bool) {
	if s.ScrollKey(key) {
		return SelectionScroll, true
	}
	if dialog && key == "y" {
		key = "c"
	}
	var intent SelectionIntent
	switch key {
	case "\x1b":
		intent = SelectionClear
	case "r", "R":
		intent = SelectionReference
	case "c", "C", "\x03":
		intent = SelectionCopy
	default:
		return 0, false
	}
	if s.dragging && intent != SelectionClear {
		return 0, dialog
	}
	return intent, true
}

// Document retains off-screen rows in the same coordinate space as the press.
// The visible snapshot wins, including pinned headings and link presentation.
func (s *Selection) Document(lines []string, top, fixed int) {
	if len(lines) <= s.Rect.H {
		return
	}
	s.screenRows = len(s.Rows)
	// Diff can start near EOF with blank viewport rows below its last line.
	// Keep that offset instead of moving the snapshot over earlier source rows.
	top = max(0, top)
	lines = append(slices.Clone(lines), make([]string, max(0, top+s.Rect.H-len(lines)))...)
	rows := make([]string, s.Rect.Y+len(lines))
	copySource := make([][]terminalui.CopySpan, len(rows))
	for i, line := range lines {
		rows[s.Rect.Y+i], copySource[s.Rect.Y+i] = terminalui.ExtractCopy(strings.Repeat(" ", s.Rect.X) + line)
	}
	s.top, s.fixed = top, fixed
	for y := s.Rect.Y; y < s.Rect.Y+s.Rect.H && y < len(s.Rows); y++ {
		at := s.DocumentY(y)
		rows[at] = s.Rows[y]
		if y < len(s.CopySource) {
			copySource[at] = s.CopySource[y]
		}
	}
	s.Rows, s.CopySource = rows, copySource
	s.startY, s.endY = s.DocumentY(s.startY), s.DocumentY(s.endY)
}

func (s *Selection) Restart(x, y int) *Selection {
	next := *s
	next.startX, next.endX = x, x
	next.startY, next.endY = s.DocumentY(y), s.DocumentY(y)
	next.dragging, next.moved = true, false
	return &next
}

func (s *Selection) DocumentY(y int) int {
	if y < s.Rect.Y+s.fixed {
		return y
	}
	return y + s.top
}

func (s *Selection) scroll(delta int) {
	if s.screenRows == 0 {
		return
	}
	s.top = max(0, min(s.top+delta, len(s.Rows)-s.Rect.Y-s.Rect.H))
}

// Mouse shares wheel extension and edge-drag scrolling across panes and dialogs.
func (s *Selection) Mouse(button, x, y int, release bool) bool {
	if s.screenRows != 0 && !release && button&64 != 0 && (s.dragging || s.Rect.Contains(x, y)) {
		delta := 3
		if button&1 == 0 {
			delta = -delta
		}
		s.scroll(delta)
		if s.dragging {
			s.Move(x, y, false)
		}
		return true
	}
	if s.dragging && (release || button&32 != 0 && button&3 == 0) {
		if !release {
			if y < s.Rect.Y+s.fixed {
				s.scroll(-1)
			} else if y >= s.Rect.Y+s.Rect.H {
				s.scroll(1)
			}
		}
		s.Move(x, y, release)
		return true
	}
	return false
}

func (s *Selection) ScrollKey(key string) bool {
	delta := 0
	switch key {
	case "\x1b[A":
		delta = -1
	case "\x1b[B":
		delta = 1
	case "\x1b[5~":
		delta = -max(1, s.Rect.H-s.fixed-1)
	case "\x1b[6~":
		delta = max(1, s.Rect.H-s.fixed-1)
	case "\x1b[H", "\x1b[1~":
		delta = -len(s.Rows)
	case "\x1b[F", "\x1b[4~":
		delta = len(s.Rows)
	default:
		return false
	}
	if s.screenRows == 0 {
		return false
	}
	x, y := s.endX, s.endY-s.top
	if s.endY < s.Rect.Y+s.fixed {
		y = s.endY
	}
	s.scroll(delta)
	if s.dragging {
		s.Move(x, y, false)
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

// Ordered returns the selection's first and last rows.
func (s *Selection) Ordered() (int, int, int, int) {
	ax, ay, bx, by := s.startX, s.startY, s.endX, s.endY
	if ay > by || ay == by && ax > bx {
		ax, ay, bx, by = bx, by, ax, ay
	}
	return ax, ay, bx, by
}

// Bounds is the selected text of row y, as a half-open column range.
func (s *Selection) Bounds(y int) (int, int) {
	ax, ay, bx, by := s.Ordered()
	if y < ay || y > by {
		return 0, 0
	}
	var left, right int
	annotated := false
	if y < len(s.CopySource) {
		for _, span := range s.CopySource[y] {
			if span.Column < s.Rect.X || span.Column >= s.Rect.X+s.Rect.W {
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
		right = min(right, s.Rect.X+s.Rect.W)
	} else if y < len(s.ContentLeft) {
		// Dialog rows have explicit gutters; glyphs inside their content are
		// literal output, including box drawing and block characters.
		left = max(s.Rect.X, s.ContentLeft[y])
		right = min(s.Rect.X+s.Rect.W, ansi.StringWidth(strings.TrimRight(ansi.Strip(s.Rows[y]), " ")))
	} else {
		left, right = selectionSpan(s.Rows[y], s.Rect.X, s.Rect.X+s.Rect.W)
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
	for g := uniseg.NewGraphemes(ansi.Strip(s.Rows[y])); g.Next(); {
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

// Move follows a drag within its snapshot, including a release outside it.
func (s *Selection) Move(x, y int, release bool) {
	s.endX = min(max(x, s.Rect.X), s.Rect.X+s.Rect.W-1)
	s.endY = s.DocumentY(min(max(y, s.Rect.Y), s.Rect.Y+s.Rect.H-1))
	s.moved = s.moved || s.endX != s.startX || s.endY != s.startY
	if release {
		s.dragging = false
	}
}

// Row paints the selection without retaining highlight escapes in its snapshot.
func (s *Selection) Row(y int, theme diffrender.Theme) string {
	row := s.Rows[y]
	if left, right := s.Bounds(y); s.moved && right > left {
		return ansi.Cut(row, 0, left) + activityui.TextSelection(ansi.Cut(row, left, right), theme) + ansi.Cut(row, right, ansi.StringWidth(row))
	}
	return row
}

// Text uses the frozen visible annotations. Unannotated UI rows retain
// ordinary selection behavior, but never supply table contents or code fill.
func (s *Selection) Text() string {
	_, first, _, last := s.Ordered()
	var parts []terminalui.CopyFragment
	var out []string
	flush := func() {
		if len(parts) > 0 {
			out = append(out, terminalui.CopyText(parts))
			parts = nil
		}
	}
	ax, ay, bx, by := s.Ordered()
	for y := first; y <= last && y < len(s.Rows); y++ {
		left, right := s.Rect.X, s.Rect.X+s.Rect.W
		if y == ay {
			left = max(left, ax)
		}
		if y == by {
			right = min(right, bx+1)
		}
		annotated := false
		if y < len(s.CopySource) {
			for _, span := range s.CopySource[y] {
				// Ignore metadata belonging to another pane in this composed screen row.
				if span.Column < s.Rect.X || span.Column >= s.Rect.X+s.Rect.W {
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
		if l, r := s.Bounds(y); r > l {
			line = strings.TrimRight(ansi.Strip(ansi.Cut(s.Rows[y], l, r)), " ")
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
