package terminalview

import (
	"cmp"
	"slices"

	presentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/terminalui"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

func tokenStyle(kind presentation.TokenKind) string {
	switch kind {
	case presentation.FileToken:
		return "\x1b[1m" + terminalui.Green
	case presentation.SkillToken:
		return "\x1b[1m" + terminalui.Amber
	case presentation.SelectionToken:
		return "\x1b[1;36m"
	default:
		return "\x1b[1;35m"
	}
}

type TextPoint struct{ Offset, Row, Column int }

// LayoutSpans keeps tokens together when they fit a row. Oversized tokens wrap
// at grapheme boundaries without losing text. Composer and transcript share it.
func LayoutSpans(text string, spans []presentation.TextSpan, width int) ([]string, []TextPoint) {
	width = max(1, width)
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(a, b presentation.TextSpan) int { return cmp.Compare(a.Start, b.Start) })
	rows := []string{""}
	var points []TextPoint
	column := 0
	newline := func() { rows = append(rows, ""); column = 0 }
	emit := func(start, end int, style string) {
		if style != "" {
			size := ansi.StringWidth(textsanitize.Safe(text[start:end], false))
			if column > 0 && size <= width && column+size > width {
				newline()
			}
		}
		g := uniseg.NewGraphemes(text[start:end])
		for g.Next() {
			offset, _ := g.Positions()
			shown := textsanitize.Safe(g.Str(), false)
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
