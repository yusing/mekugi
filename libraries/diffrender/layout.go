package diffrender

// Source: internal/livediff/render.go:107:129,243:399;model.go:373:394@[ebde9ad5]
import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// GutterWidth is the display width of Gutter.
const GutterWidth = 2

// LineSource attributes a display row. Chrome rows have no Line.
type LineSource struct {
	// Change is the caller-supplied source reference; rendering does not interpret it.
	Change, Path string
	Line         int  // Gutter coordinate: old for deletions, new otherwise.
	Deleted      bool // Line is an old-file coordinate.
	Content      int  // Display column where the row's text starts, after gutters.
}

// Numbers lays out the line-number gutter. Old and New select the old- and
// new-file columns; a row without a coordinate on a side leaves it blank, so a
// number never changes meaning between rows.
type Numbers struct {
	Digits   int
	Old, New bool
}

// NumbersFor selects the sides rows have: an added file shows only new
// numbers, a deleted file only old ones, and other files both.
func NumbersFor(rows []diffmodel.ReviewRow, digits int) Numbers {
	numbers := Numbers{Digits: digits}
	for _, row := range rows {
		numbers.Old = numbers.Old || row.Kind != '+'
		numbers.New = numbers.New || row.Kind != '-'
	}
	return numbers
}

// Union keeps every column and the wider number width.
func (n Numbers) Union(other Numbers) Numbers {
	return Numbers{max(n.Digits, other.Digits), n.Old || other.Old, n.New || other.New}
}

// Fit drops the gutter when it would leave the source fewer than two columns.
func (n Numbers) Fit(width int) Numbers {
	if n.Width() == 0 || width-4-n.Width() < 2 {
		return Numbers{}
	}
	return n
}

// Width includes the separator; zero means no gutter.
func (n Numbers) Width() int {
	if n.Digits == 0 || !n.Old && !n.New {
		return 0
	}
	if n.Old && n.New {
		return 2*n.Digits + 2
	}
	return n.Digits + 1
}

// Format paints one row's coordinates; zero leaves a column blank.
func (n Numbers) Format(old, new int) string {
	if n.Width() == 0 {
		return ""
	}
	column := func(number int) string {
		if number <= 0 {
			return strings.Repeat(" ", n.Digits)
		}
		text := strconv.Itoa(number)
		return strings.Repeat(" ", max(0, n.Digits-len(text))) + text
	}
	text := ""
	if n.Old {
		text = column(old)
	}
	if n.Old && n.New {
		text += " "
	}
	if n.New {
		text += column(new)
	}
	return Subtle + text + "│" + SubtleReset
}

// Continuation paints the gutter of a wrapped row or an annotation.
func (n Numbers) Continuation() string {
	return n.Format(0, 0)
}

// Layout keeps terminal geometry and its deferred syntax in one owner.
type Layout struct {
	syntaxHunks   []syntaxHunk
	renderedBytes int
	Lines         []string
	Hunks         []int
	RowStarts     []int
	Sources       []LineSource
}

// AppendLine adds one chrome or source row with the shared gutter and width bound.
func (render *Layout) AppendLine(ctx context.Context, theme Theme, width int, line string, highlighted, continuation bool, source LineSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	line = ansi.Truncate(Gutter(highlighted, theme)+line, max(0, width-1), "")
	render.renderedBytes += len(line) + 1
	if render.renderedBytes > MaxSourceBytes {
		return errors.New("live diff rendering exceeds 64 MiB; use mchanges with a narrower range")
	}
	if !continuation {
		render.RowStarts = append(render.RowStarts, len(render.Lines))
	}
	render.Lines = append(render.Lines, line)
	render.Sources = append(render.Sources, source)
	return nil
}

// AppendHunk lays out one supplied hunk; optional deferred syntax paints only
// intersecting complete hunks later without changing row geometry.
func (r *Renderer) AppendHunk(ctx context.Context, render *Layout, theme Theme, review diffmodel.ReviewFile, hunk diffmodel.ReviewHunk, width int, numbers Numbers, highlighted, layoutOnly bool, source LineSource) error {
	numbers = numbers.Fit(width)
	sourceWidth := max(1, width-4-numbers.Width())
	continuationNumbers := numbers.Continuation()
	render.Hunks = append(render.Hunks, len(render.Lines))
	rows := hunk.Rows
	if !layoutOnly {
		before, after, err := r.ColorHunk(ctx, theme, review, hunk.Rows)
		if err != nil {
			return err
		}
		rows = slices.Clone(rows)
		diffmodel.AlignRows(rows, before, after)
	}
	var paint syntaxHunk
	if layoutOnly {
		paint = syntaxHunk{renderer: r, theme: theme, review: review, rows: hunk.Rows, numbers: numbers,
			before: hunk.BeforeStart + 1, after: hunk.AfterStart + 1,
			start: len(render.Lines), width: width, sourceWidth: sourceWidth, highlighted: highlighted}
	}
	oldLine, newLine := hunk.BeforeStart+1, hunk.AfterStart+1
	for n, row := range rows {
		text := row.Text
		if layoutOnly {
			text = textsanitize.Safe(strings.TrimSuffix(row.Text, "\n"), false)
		}
		coordinate, rowNumbers := rowCoordinates(numbers, row.Kind, &oldLine, &newLine)
		source.Line, source.Deleted = coordinate, row.Kind == '-'
		for n, line := range sourceFragments(theme, width, sourceWidth, rowNumbers, continuationNumbers, text, row.Kind) {
			prefix := rowNumbers
			if n > 0 {
				prefix = continuationNumbers
			}
			source.Content = GutterWidth + ansi.StringWidth(prefix)
			if err := render.AppendLine(ctx, theme, width, line, highlighted, n > 0, source); err != nil {
				return err
			}
		}
		source.Line, source.Deleted, source.Content = 0, false, GutterWidth
		if !strings.HasSuffix(hunk.Rows[n].Text, "\n") {
			if err := render.AppendLine(ctx, theme, width, Subtle+"\\ No newline at end of file"+SubtleReset, highlighted, false, source); err != nil {
				return err
			}
		}
	}
	if layoutOnly {
		paint.end = len(render.Lines)
		render.syntaxHunks = append(render.syntaxHunks, paint)
	}
	return nil
}

// rowCoordinates advances both sides past one row. The source coordinate is
// old for deletions and new otherwise.
func rowCoordinates(numbers Numbers, kind byte, oldLine, newLine *int) (int, string) {
	old, next := 0, 0
	if kind != '+' {
		old = *oldLine
		*oldLine++
	}
	if kind != '-' {
		next = *newLine
		*newLine++
	}
	coordinate := next
	if kind == '-' {
		coordinate = old
	}
	return coordinate, numbers.Format(old, next)
}

// Source wrapping is shared by layout and decoration, so syntax cannot change geometry.
func sourceFragments(theme Theme, width, sourceWidth int, numbers, continuationNumbers, text string, kind byte) []string {
	var lines []string
	for _, fragment := range WrapSource(text, sourceWidth) {
		prefix := numbers
		if len(lines) > 0 {
			prefix = continuationNumbers
		}
		lines = append(lines, SourceLine(theme, width, prefix, fragment, kind))
	}
	return lines
}

type syntaxHunk struct {
	renderer                                      *Renderer
	theme                                         Theme
	review                                        diffmodel.ReviewFile
	rows                                          []diffmodel.ReviewRow
	numbers                                       Numbers
	before, after, start, end, width, sourceWidth int
	highlighted, painted                          bool
}

// PaintViewport decorates intersecting complete hunks. Coordinates and source
// attribution were fixed during layout; off-screen hunks never invoke a lexer.
func (render *Layout) PaintViewport(ctx context.Context, start, end int) error {
	hunks := render.viewportHunks(start, end)
	for i := range hunks {
		h := &hunks[i]
		if h.painted {
			continue
		}
		before, after, err := h.renderer.ColorHunk(ctx, h.theme, h.review, h.rows)
		if err != nil {
			return err
		}
		var lines []string
		rows := slices.Clone(h.rows)
		diffmodel.AlignRows(rows, before, after)
		oldLine, newLine := h.before, h.after
		continuation := h.numbers.Continuation()
		for n, row := range rows {
			_, numbers := rowCoordinates(h.numbers, row.Kind, &oldLine, &newLine)
			lines = append(lines, sourceFragments(h.theme, h.width, h.sourceWidth, numbers, continuation, row.Text, row.Kind)...)
			if !strings.HasSuffix(h.rows[n].Text, "\n") {
				lines = append(lines, Subtle+"\\ No newline at end of file"+SubtleReset)
			}
		}
		if len(lines) != h.end-h.start {
			return errors.New("live diff syntax changed layout")
		}
		delta := 0
		for n, line := range lines {
			lines[n] = ansi.Truncate(Gutter(h.highlighted, h.theme)+line, max(0, h.width-1), "")
			delta += len(lines[n]) - len(render.Lines[h.start+n])
		}
		if render.renderedBytes+delta > MaxSourceBytes {
			return errors.New("live diff rendering exceeds 64 MiB; use mchanges with a narrower range")
		}
		copy(render.Lines[h.start:h.end], lines)
		render.renderedBytes += delta
		h.painted = true
	}
	return nil
}

// Hunks are appended in row order and never overlap. Bound the decoration walk
// before visiting hunks, including already-painted ones.
func (render *Layout) viewportHunks(start, end int) []syntaxHunk {
	if end <= start {
		return nil
	}
	hunks := render.syntaxHunks
	first := sort.Search(len(hunks), func(i int) bool { return hunks[i].end > start })
	hunks = hunks[first:]
	last := sort.Search(len(hunks), func(i int) bool { return hunks[i].start >= end })
	return hunks[:last]
}
