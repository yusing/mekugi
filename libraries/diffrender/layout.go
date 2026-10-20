package diffrender

// Source: internal/livediff/render.go:107:129,243:399;model.go:373:394@[ebde9ad5]
import (
	"context"
	"errors"
	"slices"
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
func (r *Renderer) AppendHunk(ctx context.Context, render *Layout, theme Theme, review diffmodel.ReviewFile, hunk diffmodel.ReviewHunk, width, digits int, highlighted, layoutOnly bool, source LineSource) error {
	numberWidth := digits + 1
	if width-3 < digits+4 {
		numberWidth = 0
	}
	sourceWidth := max(1, width-4-numberWidth)
	continuationNumbers := ""
	if numberWidth > 0 {
		continuationNumbers = Subtle + strings.Repeat(" ", digits) + "│" + SubtleReset
	}
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
		paint = syntaxHunk{renderer: r, theme: theme, review: review, rows: hunk.Rows,
			start: len(render.Lines), width: width, sourceWidth: sourceWidth, digits: digits,
			numberWidth: numberWidth, highlighted: highlighted}
	}
	oldLine, newLine := hunk.BeforeStart+1, hunk.AfterStart+1
	for n, row := range rows {
		coordinate, text := 0, row.Text
		if layoutOnly {
			text = textsanitize.Safe(strings.TrimSuffix(row.Text, "\n"), false)
			paint.rowStarts = append(paint.rowStarts, len(render.Lines))
		}
		if row.Kind != '+' {
			coordinate = oldLine
			oldLine++
		}
		if row.Kind != '-' {
			coordinate = newLine
			newLine++
		}
		number := strconv.Itoa(coordinate)
		source.Line, source.Deleted = coordinate, row.Kind == '-'
		numbers := ""
		if numberWidth > 0 {
			numbers = Subtle + strings.Repeat(" ", max(0, digits-len(number))) + number + "│" + SubtleReset
		}
		for n, line := range sourceFragments(theme, width, sourceWidth, numbers, continuationNumbers, text, row.Kind) {
			prefix := numbers
			if n > 0 && numbers != "" {
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

// Source wrapping is shared by layout and decoration, so syntax cannot change geometry.
func sourceFragments(theme Theme, width, sourceWidth int, numbers, continuationNumbers, text string, kind byte) []string {
	var lines []string
	for _, fragment := range WrapSource(text, sourceWidth) {
		prefix := numbers
		if len(lines) > 0 && numbers != "" {
			prefix = continuationNumbers
		}
		lines = append(lines, SourceLine(theme, width, prefix, fragment, kind))
	}
	return lines
}

type syntaxHunk struct {
	renderer                                            *Renderer
	theme                                               Theme
	review                                              diffmodel.ReviewFile
	rows                                                []diffmodel.ReviewRow
	rowStarts                                           []int
	start, end, width, sourceWidth, digits, numberWidth int
	highlighted, painted                                bool
}

// PaintViewport decorates intersecting complete hunks. Coordinates and source
// attribution were fixed during layout; off-screen hunks never invoke a lexer.
func (render *Layout) PaintViewport(ctx context.Context, start, end int) error {
	if end <= start {
		return nil
	}
	for i := range render.syntaxHunks {
		h := &render.syntaxHunks[i]
		if h.painted || h.end <= start || h.start >= end {
			continue
		}
		before, after, err := h.renderer.ColorHunk(ctx, h.theme, h.review, h.rows)
		if err != nil {
			return err
		}
		var lines []string
		rows := slices.Clone(h.rows)
		diffmodel.AlignRows(rows, before, after)
		for n, row := range rows {
			text := row.Text
			numbers, continuation := "", ""
			if h.numberWidth > 0 {
				number := strconv.Itoa(render.Sources[h.rowStarts[n]].Line)
				numbers = Subtle + strings.Repeat(" ", max(0, h.digits-len(number))) + number + "│" + SubtleReset
				continuation = Subtle + strings.Repeat(" ", h.digits) + "│" + SubtleReset
			}
			lines = append(lines, sourceFragments(h.theme, h.width, h.sourceWidth, numbers, continuation, text, row.Kind)...)
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
