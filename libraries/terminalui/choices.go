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
type ChoiceRow struct {
	Text, Description string
	// Status repeats a state the description states, such as "current", beside
	// the label when the description column does not fit.
	Status string
	// Danger rows take ChoiceListView.Danger; Muted rows are dim.
	Danger, Muted bool
}
type ChoiceListView struct {
	Rows                      []ChoiceRow
	Selected                  int
	Query, Selection, Problem string
	// Marker, when set, colors a pointer and the label of the selected row
	// instead of banding it with Selection, as docked panels mark their choice.
	Marker string
	// Danger, when set, colors destructive rows and the problem text. A
	// selected destructive row keeps it, in bold, instead of Marker.
	Danger  string
	Loading bool
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
	problem := Dim
	if v.Danger != "" {
		problem = v.Danger
	}
	if len(v.Rows) == 0 {
		message, style := "no matches", Dim
		if v.Loading {
			message = "loading..."
		}
		if v.Problem != "" {
			message, style = v.Problem, problem
		}
		put(first, "  "+style+choiceText(message)+Reset)
		return
	}
	columns := 0
	for _, row := range v.Rows[l.Top:min(len(v.Rows), l.Top+visible)] {
		columns = max(columns, ansi.StringWidth(choiceText(row.Text)))
	}
	describe := width-(columns+4) >= 24
	for row := 0; row < visible && l.Top+row < len(v.Rows); row++ {
		i := l.Top + row
		item := v.Rows[i]
		label, description := choiceText(item.Text), choiceText(item.Description)
		selected := i == v.Selected
		style := ""
		switch {
		case item.Danger && v.Danger != "":
			style = v.Danger
		case item.Muted:
			style = Dim
		}
		marker := v.Marker
		if marker != "" && item.Danger && v.Danger != "" {
			marker = v.Danger + "\x1b[1m"
		}
		prefix := "  "
		switch {
		case selected && marker != "":
			prefix = marker + "❯" + Reset + " "
		case selected:
			prefix = "› "
		}
		status := ""
		if !describe && item.Status != "" {
			status = Dim + " · " + choiceText(item.Status) + Reset
		}
		text := choiceHighlight(label, v.Query)
		if room := width - 2 - ansi.StringWidth(status); status != "" && ansi.StringWidth(label) > room {
			text = ansi.Truncate(text, max(1, room), "…")
		}
		switch {
		case selected && marker != "":
			text = prefix + marker + text + Reset
		case style != "":
			text = prefix + style + text + Reset
		default:
			text = prefix + text
		}
		text += status
		if description != "" && describe {
			text += strings.Repeat(" ", max(0, columns-ansi.StringWidth(label))+2) + Dim + description + Reset
		}
		text = ansi.Truncate(text, width, "…")
		if selected && marker == "" {
			text = v.Selection + ansi.Strip(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + Reset
		}
		put(first+row, text)
	}
	if v.Problem != "" {
		text := "  " + choiceText(v.Problem)
		if v.Danger != "" {
			text = "  " + v.Danger + choiceText(v.Problem) + Reset
		}
		put(len(frame)-2, text)
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
