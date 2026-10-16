package router

import (
	"strings"

	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// Source: codex-rs/tui/src/bottom_pane/{picker_rows,skill_popup,
// skills_toggle_view,list_selection_view}.rs@86be5320b068ef67b56348b02aa8c33706955da6.
// Modal skill workflows replace the composer. Completions keep it visible.
func (u *appServerUI) pickerHeight(width int) int {
	p := &u.picker
	if p.modal == "archive" {
		return min(8, max(1, len(p.Choices))) + 3 + len(pickerWrap(u.archivePrompt(), max(1, width-4)))
	}
	return conversationui.PickerHeight(len(p.Choices), conversationui.PickerOptions{SkillsMenu: p.modal == "menu", Manage: p.modal == "manage"}, width)
}

func pickerWrap(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func pickerText(text string) string {
	return strings.ReplaceAll(textsanitize.Safe(text, false), "\n", " ")
}

func (u *appServerUI) pickerSelection() string {
	if u.view != nil && u.view.Painter.Colors.HasBackground {
		if u.view.Painter.Theme == diffrender.LightTheme {
			return "\x1b[1;38;2;0;0;46;48;2;164;205;251m"
		}
		return "\x1b[1;38;2;0;0;46;48;2;99;168;248m"
	}
	return "\x1b[1;7m"
}

func (u *appServerUI) skillsModalFrame(width, height int) []string {
	ph := min(height, u.pickerHeight(width))
	frame := make([]string, height)
	u.mainContentPainted = false
	u.composerRect = terminalsession.Rect{}
	if height > ph {
		copy(frame, u.transcriptView().Render(width, height-ph, u.now()))
	}
	copy(frame[height-ph:], u.renderPicker(width, ph))
	u.picker.rect = terminalsession.Rect{0, height - ph, width, ph}
	return frame
}

func (u *appServerUI) renderPicker(width, height int) []string {
	p := &u.picker
	if p.modal == "" || p.modal == "menu" || p.modal == "manage" {
		band := ""
		if p.modal == "manage" {
			band = activityui.UserBand(u.view.Painter.Theme)
		}
		return conversationui.Picker(p.View(), &p.ChoiceListLayout, conversationui.PickerOptions{SkillsMenu: p.modal == "menu", Manage: p.modal == "manage", Saving: p.toggleID != "", QueueCompact: strings.TrimSpace(u.Text) == "/compact", Selection: u.pickerSelection(), Band: band}, width, height)
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
		switch p.modal {
		case "archive":
			put(0, "  "+bold+"Archive this session?"+reset)
			if height >= 5 {
				for _, line := range pickerWrap(u.archivePrompt(), max(1, width-4)) {
					if first >= height-3 {
						break
					}
					put(first, "  "+dim+line+reset)
					first++
				}
			}
			footer = "  ↑/↓ navigate · enter select · esc cancel"
		case "settings":
			put(0, "  "+bold+"Choose "+strings.TrimPrefix(u.settingsChoices, "/")+reset)
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
	p.ChoiceListLayout.Draw(frame, width, first, visible, terminalui.ChoiceListView{Rows: rows, Selected: p.Selected, Selection: u.pickerSelection(), Problem: p.Problem, Loading: p.Loading})
	if p.toggleID != "" {
		put(height-2, "  "+dim+"Saving…"+reset)
	}
	return frame
}
