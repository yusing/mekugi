// Source: apps/mekugi/internal/router/{app_server_composer,app_server_shell}.go@[ce8700a8] draft painting and navigation layout.
package terminalsession

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// DraftSpans supplies the same atomic token annotations for editing and display.
func DraftSpans(d composer.Draft) []activitypresentation.TextSpan {
	var spans []activitypresentation.TextSpan
	for _, span := range d.Spans() {
		kind := activitypresentation.ImageToken
		switch span.Kind {
		case composer.File:
			kind = activitypresentation.FileToken
		case composer.Skill:
			kind = activitypresentation.SkillToken
		case composer.Selection:
			kind = activitypresentation.SelectionToken
		}
		spans = append(spans, activitypresentation.TextSpan{Start: span.Start, End: span.End, Kind: kind})
	}
	return spans
}

// Layout and navigation share the same atomic-span layout as submitted input.
func DraftLayout(d composer.Draft, width int) ([]string, []activityui.TextPoint) {
	if width == 0 {
		width = 80
	}
	rows, points := activityui.LayoutSpans(d.Text, DraftSpans(d), width)
	last := points[len(points)-1]
	if width > 1 && last.Column >= width {
		rows = append(rows, "")
		points[len(points)-1] = activityui.TextPoint{Offset: len(d.Text), Row: len(rows) - 1}
	}
	if width > 1 {
		rows[len(rows)-1] += " "
	}
	return rows, points
}

// Paint complete logical lines before cutting them to the existing draft layout.
// Soft wraps must not change Bash's multiline lexical state or cursor geometry.
func PaintShellDraft(rows []string, text string, painter *terminalui.Painter) {
	// Highlight returns cached rows. The mode marker belongs only to this draft.
	lines := slices.Clone(painter.Highlight("bash", textsanitize.Safe(text[1:], false)+"\n"))
	lines[0] = "!" + lines[0]
	line, column := 0, 0
	for i, row := range rows {
		if line >= len(lines) {
			break // The layout can add an empty row for the trailing caret.
		}
		width := ansi.StringWidth(row)
		painted := ansi.Cut(lines[line], column, column+width)
		rows[i] = painted + "\x1b[39m" + strings.Repeat(" ", max(0, width-ansi.StringWidth(painted)))
		column += width
		if column >= ansi.StringWidth(lines[line]) {
			line++
			column = 0
		}
	}
}
