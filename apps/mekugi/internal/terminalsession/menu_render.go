package terminalsession

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

func (m *Menu) Height(width int) int {
	if m.body == nil {
		return 0
	}
	p := m.body
	if strings.HasPrefix(string(p.Kind), "preset") {
		return len(presetHeader(p, width)) + max(1, len(p.Choices)) + 3
	}
	if p.Kind == "archive" || p.Kind == "rewind" || p.Kind == "side-replace" {
		return min(8, max(1, len(p.Choices))) + 3 + len(panelWrap(p.Explanation, max(1, width-4)))
	}
	return conversationui.PickerHeight(len(p.Choices), conversationui.PickerOptions{SkillsMenu: p.Kind == "menu", Manage: p.Kind == "manage"}, width)
}

// Source: apps/mekugi/internal/router/app_server_picker_render.go:59:113@932125e0.
func (m *Menu) Render(painter *terminalui.Painter, selection string, width, height int) []string {
	p := m.body
	if strings.HasPrefix(string(p.Kind), "preset") {
		return m.renderPreset(selection, width, height)
	}
	if p.Kind == "" || p.Kind == "menu" || p.Kind == "manage" {
		band := ""
		if p.Kind == "manage" {
			band = activityui.UserBand(painter.Theme)
		}
		return conversationui.Picker(p.View, &m.ChoiceListLayout, conversationui.PickerOptions{Selected: m.Selected, SkillsMenu: p.Kind == "menu", Manage: p.Kind == "manage", Saving: p.Saving, QueueCompact: p.QueueCompact, Selection: selection, Band: band}, width, height)
	}
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	bold, dim, reset := "\x1b[1m", terminalui.Dim, terminalui.Reset
	first, last := 0, height
	if height >= 3 {
		first, last = 1, height-2
	}
	if height > 1 {
		footer := ""
		switch p.Kind {
		case "archive", "rewind", "side-replace":
			put(0, "  "+bold+p.Title+reset)
			if height >= 5 {
				for _, line := range panelWrap(p.Explanation, max(1, width-4)) {
					if first >= height-3 {
						break
					}
					put(first, "  "+dim+line+reset)
					first++
				}
			}
			footer = "  ↑/↓ navigate · enter select · esc cancel"
		case "settings":
			put(0, "  "+bold+"Choose "+strings.TrimPrefix(p.Setting, "/")+reset)
			footer = "  ↑/↓ navigate · enter apply · esc cancel"
		case "copy":
			put(0, "  "+bold+"Copy to clipboard"+reset)
			footer = "  " + bold + "↑/↓" + reset + dim + " navigate · " + reset + bold + "enter" + reset + dim + " copy · " + reset + bold + "esc" + reset + dim + " close" + reset
		}
		put(height-1, footer)
	}
	visible := min(8, max(0, last-first))
	if height < 3 {
		visible, first = height, 0
	}
	rows := make([]terminalui.ChoiceRow, len(p.Choices))
	for i, choice := range p.Choices {
		rows[i] = terminalui.ChoiceRow{Text: choice.Label(), Description: choice.Description}
	}
	m.ChoiceListLayout.Draw(frame, width, first, visible, terminalui.ChoiceListView{Rows: rows, Selected: m.Selected, Selection: selection, Problem: p.Problem, Loading: p.Loading})
	return frame
}

// SelectionStyle shares the stock choice treatment across menus and prompt docks.
// Source: apps/mekugi/internal/router/app_server_picker_render.go:36:44@932125e0.
func SelectionStyle(painter *terminalui.Painter) string {
	if painter != nil && painter.Colors.HasBackground {
		if painter.Theme == diffrender.LightTheme {
			return "\x1b[1;38;2;0;0;46;48;2;164;205;251m"
		}
		return "\x1b[1;38;2;0;0;46;48;2;99;168;248m"
	}
	return "\x1b[1;7m"
}

// ModalFrame combines terminal transcript rows with the menu surface. It owns
// no source feed, acquisition or backend effect.
func (m *Menu) ModalFrame(view *activityui.View, now time.Time, width, height int) []string {
	if m.Dialog() {
		return m.dialogFrame(view, now, width, height)
	}
	ph := min(height, m.Height(width))
	frame := make([]string, height)
	if height > ph {
		copy(frame, view.Render(width, height-ph, now))
	}
	copy(frame[height-ph:], m.Render(&view.Painter.Painter, SelectionStyle(&view.Painter.Painter), width, ph))
	m.Rect = Rect{0, height - ph, width, ph}
	return frame
}

func (m *Menu) Dialog() bool { return m.body != nil && m.body.Dialog }

func (m *Menu) dialogFrame(view *activityui.View, now time.Time, width, height int) []string {
	frame := view.Render(width, height, now)
	w, h := dialogSize(width, height)
	if w < 12 || h < terminalui.DialogChrome+1 {
		m.Rect = Rect{0, 0, width, height}
		return m.Render(&view.Painter.Painter, SelectionStyle(&view.Painter.Painter), width, height)
	}
	p := m.body
	explanation := panelWrap(p.Explanation, w-4)
	h = min(h, terminalui.DialogChrome+len(explanation)+1+min(8, max(1, len(p.Choices))))
	body := make([]string, h-terminalui.DialogChrome)
	first := min(len(explanation), max(0, len(body)-2))
	copy(body, explanation[:first])
	if first > 0 {
		first++
	}
	rows := make([]terminalui.ChoiceRow, len(p.Choices))
	for i, choice := range p.Choices {
		rows[i] = terminalui.ChoiceRow{Text: choice.Label()}
	}
	m.ChoiceListLayout.Draw(body, w-4, first, len(body)-first, terminalui.ChoiceListView{Rows: rows, Selected: m.Selected, Selection: SelectionStyle(&view.Painter.Painter), Problem: p.Problem, Loading: p.Loading})
	m.Rect = Rect{(width - w) / 2, (height - h) / 2, w, h}
	m.RowStart += 3 // Title, detail and separator precede the body.
	dialog := terminalui.DialogFrame{Page: terminalui.DialogPage{Title: p.Title}, Rows: body, Footer: "↑/↓ choose · enter select · esc cancel"}
	for i := range frame {
		frame[i] = terminalui.Backdrop(frame[i])
	}
	for i, row := range view.Painter.Dialog(dialog, w, h) {
		y := m.Rect.Y + i
		left := ansi.Cut(frame[y], 0, m.Rect.X)
		left += strings.Repeat(" ", max(0, m.Rect.X-ansi.StringWidth(left)))
		frame[y] = left + terminalui.Reset + row + terminalui.Reset + ansi.Cut(frame[y], m.Rect.X+w, width) + terminalui.Reset
	}
	return frame
}

// presetListWidth bounds the choice column so long presets truncate and the
// selection band stays beside the list instead of spanning wide terminals.
const presetListWidth = 64

// presetEditorCopy replaces the shared Edit explanation, which describes the
// browser's inline editing.
const presetEditorCopy = "Write a prompt or slash command in your external editor, then save and close it."

// renderPreset paints title, context, keyed choices, a problem row and a
// footer. Context yields to choices when the terminal is short; a list that
// still scrolls gains a spacer for its upper marker.
func (m *Menu) renderPreset(selection string, width, height int) []string {
	p := m.body
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	rows := presetRows(p)
	listWidth := width
	if len(rows) > 0 {
		widest := 0
		for _, row := range rows {
			widest = max(widest, ansi.StringWidth(panelText(row.Text)))
		}
		listWidth = min(width, presetListWidth, widest+4) // Pointer prefix and a trailing margin.
	}
	view := terminalui.ChoiceListView{Rows: rows, Selected: m.Selected, Selection: selection, Problem: p.Problem, Loading: p.Loading}
	if height < 5 {
		m.ChoiceListLayout.Draw(frame, listWidth, 0, height, view)
		return frame
	}
	put(0, "  \x1b[1m"+p.Title+terminalui.Reset)
	put(height-1, "  "+presetHints(p, width).Render())
	list, room := max(1, len(p.Choices)), height-3
	header := presetHeader(p, width)
	header = header[:min(len(header), max(0, room-list))]
	for i, line := range header {
		put(1+i, line)
	}
	first := 1 + len(header)
	if list > room {
		first++
	}
	m.ChoiceListLayout.Draw(frame, listWidth, first, height-2-first, view)
	if p.Problem != "" {
		put(height-2, "  "+panelText(p.Problem)) // Problems may be wider than the list.
	}
	return frame
}

// presetHeader explains the stage and previews the text it acts on, ending a
// preview with a spacer before its actions. A confirmation's explanation is
// the affected text: the saved slot, then "\n→ " and any pending replacement.
func presetHeader(p *sessionpresentation.MenuView, width int) []string {
	rail := terminalui.Dim + "│" + terminalui.Reset + " "
	if p.Kind == "preset-confirm" {
		if p.Query == "" || !strings.HasSuffix(p.Explanation, "\n→ "+p.Query) {
			return append(presetPreview(p.Explanation, width, 6, rail, rail, terminalui.Dim), "")
		}
		saved := presetPreview(strings.TrimSuffix(p.Explanation, "\n→ "+p.Query), width, 3, rail, rail, terminalui.Dim)
		return append(append(saved, presetPreview(p.Query, width, 3, "→ ", "  ", "")...), "")
	}
	explanation := p.Explanation
	if p.Kind == "preset-edit" {
		explanation = presetEditorCopy
	}
	var rows []string
	for _, line := range panelWrap(textsanitize.Safe(explanation, false), max(1, width-4)) {
		if line != "" {
			rows = append(rows, "  "+terminalui.Dim+line+terminalui.Reset)
		}
	}
	switch p.Kind {
	case "preset-edit":
		rows = append(append(rows, presetPreview(p.Query, width, 6, rail, rail, "")...), "")
	case "preset-label":
		// A single-line field keeps its end and the caret in view.
		label := panelText(p.Query)
		if over := ansi.StringWidth(label) - max(1, width-6); over > 0 {
			label = ansi.TruncateLeft(label, over+1, "…")
		}
		rows = append(rows, "  "+rail+label+"\x1b[7m \x1b[27m", "")
	}
	return rows
}

// presetPreview wraps sanitized text under a lead and continuation gutter,
// replacing rows beyond limit with a count of the hidden rows.
func presetPreview(text string, width, limit int, lead, rest, style string) []string {
	if strings.TrimSpace(text) == "" {
		return []string{"  " + lead + terminalui.Dim + "(empty)" + terminalui.Reset}
	}
	lines := panelWrap(textsanitize.Safe(text, false), max(1, width-4))
	more := 0
	if len(lines) > limit {
		more = len(lines) - limit + 1
		lines = lines[:limit-1]
	}
	rows := make([]string, 0, len(lines)+1)
	for i, line := range lines {
		gutter := rest
		if i == 0 {
			gutter = lead
		}
		rows = append(rows, "  "+gutter+style+line+terminalui.Reset)
	}
	if more > 0 {
		rows = append(rows, "  "+rest+terminalui.Dim+fmt.Sprintf("… %d more lines", more)+terminalui.Reset)
	}
	return rows
}

// presetRows prefixes each choice with the key that activates it, matching
// Menu.Key: slot digits and action letters, r for retry and y for confirmation.
// The terminal's Edit action hands off to the external editor; typing edits a
// label, so its action has no key.
func presetRows(p *sessionpresentation.MenuView) []terminalui.ChoiceRow {
	rows := make([]terminalui.ChoiceRow, len(p.Choices))
	for i, choice := range p.Choices {
		key := ""
		switch {
		case choice.Name == "retry":
			key = "r"
		case p.Kind == "preset-confirm":
			if choice.Name == "confirm" {
				key = "y"
			}
		case p.Kind == "presets" || p.Kind == "preset-slots":
			if len(choice.Name) == 1 {
				key = choice.Name
			}
		}
		label := choice.Label()
		if p.Kind == "preset-edit" && choice.Name == "continue" {
			label = "Open editor"
		}
		if p.Kind != "preset-edit" && p.Kind != "preset-label" || key != "" {
			if key != "" {
				label = strings.TrimPrefix(label, key+"  ")
			}
			label = fmt.Sprintf("%-1s  %s", key, label)
		}
		rows[i] = terminalui.ChoiceRow{Text: label, Description: choice.Description}
	}
	return rows
}

func presetHints(p *sessionpresentation.MenuView, width int) terminalui.Hints {
	esc := terminalui.Hint{Key: "esc", Label: "close"}
	switch {
	case p.Loading:
		return terminalui.Hints{esc}
	case len(p.Choices) == 1 && p.Choices[0].Name == "retry":
		return terminalui.Hints{{Key: "r", Label: "retry"}, esc}
	case p.Kind == "preset-edit":
		return terminalui.Hints{{Key: "enter", Label: "open editor"}, {Key: "esc", Label: "cancel"}}
	case p.Kind == "preset-label" && len(p.Choices) > 0:
		return terminalui.Hints{{Key: "enter", Label: strings.ToLower(p.Choices[0].Label())}, {Key: "esc", Label: "cancel"}}
	case p.Kind == "preset-confirm":
		return terminalui.Hints{{Key: "y", Label: "confirm"}, {Key: "esc", Label: "cancel"}}
	case width < 50:
		return terminalui.Hints{{Key: "enter", Label: "select"}, esc}
	}
	return terminalui.Hints{{Key: "↑/↓", Label: "navigate"}, {Key: "enter", Label: "select"}, esc}
}
