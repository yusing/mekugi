package activity

import (
	"cmp"
	"slices"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
)

// TextSpan identifies a bound, atomic token, not text resembling a placeholder.
// Offsets are UTF-8 byte boundaries; spans must not overlap.
type TokenKind uint8

const (
	ImageToken TokenKind = iota
	FileToken
	SkillToken
	SelectionToken // A screen selection quoted as one mention.
)

type TextSpan struct {
	Start, End int
	Kind       TokenKind
}

func tokenStyle(kind TokenKind) string {
	switch kind {
	case FileToken:
		return "\x1b[1m" + Green
	case SkillToken:
		return "\x1b[1m" + Amber
	case SelectionToken:
		return "\x1b[1;36m"
	default:
		return "\x1b[1;35m"
	}
}

type TextPoint struct{ Offset, Row, Column int }

// LayoutSpans keeps tokens together when they fit a row. Oversized tokens wrap
// at grapheme boundaries without losing text. Composer and transcript share it.
func LayoutSpans(text string, spans []TextSpan, width int) ([]string, []TextPoint) {
	width = max(1, width)
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(a, b TextSpan) int { return cmp.Compare(a.Start, b.Start) })
	rows := []string{""}
	var points []TextPoint
	column := 0
	newline := func() { rows = append(rows, ""); column = 0 }
	emit := func(start, end int, style string) {
		if style != "" {
			size := ansi.StringWidth(livediff.Safe(text[start:end], false))
			if column > 0 && size <= width && column+size > width {
				newline()
			}
		}
		g := uniseg.NewGraphemes(text[start:end])
		for g.Next() {
			offset, _ := g.Positions()
			shown := livediff.Safe(g.Str(), false)
			size := ansi.StringWidth(shown)
			if shown != "\n" && (column >= width || column > 0 && column+size > width) {
				newline()
			}
			points = append(points, TextPoint{start + offset, len(rows) - 1, column})
			if shown == "\n" {
				if column >= width {
					points[len(points)-1] = TextPoint{start + offset, len(rows), 0}
				}
				newline()
				continue
			}
			if style != "" {
				shown = style + shown + "\x1b[22;39m"
			}
			rows[len(rows)-1] += shown
			column += size
		}
	}
	at := 0
	for _, span := range spans {
		emit(at, span.Start, "")
		emit(span.Start, span.End, tokenStyle(span.Kind))
		at = span.End
	}
	emit(at, len(text), "")
	points = append(points, TextPoint{len(text), len(rows) - 1, column})
	return rows, points
}
