package terminalsession

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

func (m *Menu) Height(width int) int {
	if m.body == nil {
		return 0
	}
	p := m.body
	if p.Kind == "archive" || p.Kind == "rewind" || p.Kind == "side-replace" {
		return min(8, max(1, len(p.Choices))) + 3 + len(panelWrap(p.Explanation, max(1, width-4)))
	}
	return conversationui.PickerHeight(len(p.Choices), conversationui.PickerOptions{SkillsMenu: p.Kind == "menu", Manage: p.Kind == "manage"}, width)
}

// Source: apps/mekugi/internal/router/app_server_picker_render.go:59:113@932125e0.
func (m *Menu) Render(painter *terminalui.Painter, selection string, width, height int) []string {
	p := m.body
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
