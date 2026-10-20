// Source: apps/mekugi/internal/router/app_server_picker_render.go:182:257@[0a356401] shared choice-list drawing.
package terminalui

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// ChoiceListLayout retains only the visible list viewport and pointer hit rows.
// Its caller owns item meaning, selection, headers, footers and key handling.
type ChoiceListLayout struct{ Top, RowStart, RowCount int }
type ChoiceRow struct{ Text, Description string }
type ChoiceListView struct {
	Rows                      []ChoiceRow
	Selected                  int
	Query, Selection, Problem string
	Loading                   bool
}

func (l *ChoiceListLayout) Draw(frame []string, width, first, visible int, v ChoiceListView) {
	put := func(y int, text string) {
		if y >= 0 && y < len(frame) {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	l.Top = min(v.Selected, min(l.Top, max(0, len(v.Rows)-visible)))
	if v.Selected >= l.Top+visible {
		l.Top = max(0, v.Selected-visible+1)
	}
	l.RowStart, l.RowCount = first, min(visible, max(0, len(v.Rows)-l.Top))
	if l.Top > 0 {
		put(first-1, "↑")
	}
	if l.Top+visible < len(v.Rows) {
		put(first+visible, "↓")
	}
	if len(v.Rows) == 0 {
		message := "no matches"
		if v.Loading {
			message = "loading..."
		}
		if v.Problem != "" {
			message = v.Problem
		}
		put(first, "  "+Dim+choiceText(message)+Reset)
		return
	}
	columns := 0
	for _, row := range v.Rows[l.Top:min(len(v.Rows), l.Top+visible)] {
		columns = max(columns, ansi.StringWidth(choiceText(row.Text)))
	}
	for row := 0; row < visible && l.Top+row < len(v.Rows); row++ {
		i := l.Top + row
		label, description := choiceText(v.Rows[i].Text), choiceText(v.Rows[i].Description)
		prefix := "  "
		if i == v.Selected {
			prefix = "› "
		}
		text := prefix + choiceHighlight(label, v.Query)
		if description != "" && width-(columns+4) >= 24 {
			text += strings.Repeat(" ", max(0, columns-ansi.StringWidth(label))+2) + Dim + description + Reset
		}
		text = ansi.Truncate(text, width, "…")
		if i == v.Selected {
			text = v.Selection + ansi.Strip(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + Reset
		}
		put(first+row, text)
	}
	if v.Problem != "" {
		put(len(frame)-2, "  "+choiceText(v.Problem))
	}
}

func choiceText(text string) string {
	return strings.ReplaceAll(textsanitize.Safe(text, false), "\n", " ")
}

func choiceHighlight(text, query string) string {
	needle := []rune(strings.ToLower(strings.TrimPrefix(query, "!")))
	if len(needle) == 0 {
		return text
	}
	var out strings.Builder
	at := 0
	for _, r := range text {
		if at < len(needle) && strings.EqualFold(string(r), string(needle[at])) {
			fmt.Fprintf(&out, "\x1b[1m%c\x1b[22m", r)
			at++
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// TruncateGraphemes limits a display name without splitting a grapheme.
func TruncateGraphemes(text string, n int) string {
	g := uniseg.NewGraphemes(text)
	var out strings.Builder
	for i := 0; g.Next(); i++ {
		if i == n {
			out.WriteString("…")
			break
		}
		out.WriteString(g.Str())
	}
	return out.String()
}
