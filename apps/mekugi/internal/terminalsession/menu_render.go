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
	"github.com/yusing/mekugi/modules/conversation/completion"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

func (m *Menu) Height(width int) int {
	if m.body == nil {
		return 0
	}
	p := m.body
	footer := len(dockFooter(menuKeys(p, width), width)) - 1
	if strings.HasPrefix(string(p.Kind), "preset") {
		// Heading, spacer, context, choices, problem row and keys.
		return len(presetHeader(p, width-2)) + max(1, len(p.Choices)) + 4 + footer
	}
	// Prompt panels: heading, spacer, explanation and its spacer, choices,
	// spacer and keys.
	if inlinePrompt(p.Kind) || p.Kind == sessionpresentation.MenuSettings || p.Kind == sessionpresentation.MenuCopy {
		height := min(8, max(1, len(p.Choices))) + 4 + footer
		if inlinePrompt(p.Kind) {
			if explanation := len(promptExplanation(p, width)); explanation > 0 {
				height += explanation + 1
			}
		}
		return height
	}
	return conversationui.PickerHeight(len(p.Choices), conversationui.PickerOptions{SkillsMenu: p.Kind == "menu", Manage: p.Kind == "manage"}, width)
}

// Source: apps/mekugi/internal/router/app_server_picker_render.go:59:113@932125e0.
func (m *Menu) Render(painter *terminalui.Painter, selection string, width, height int) []string {
	p := m.body
	m.closeShown = false
	if strings.HasPrefix(string(p.Kind), "preset") {
		return m.renderPreset(painter, width, height)
	}
	if p.Kind == "" || p.Kind == "menu" || p.Kind == "manage" {
		band := ""
		if p.Kind == "manage" {
			band = activityui.UserBand(painter.Theme)
		}
		return conversationui.Picker(p.View, &m.ChoiceListLayout, conversationui.PickerOptions{Selected: m.Selected, SkillsMenu: p.Kind == "menu", Manage: p.Kind == "manage", Saving: p.Saving, QueueCompact: p.QueueCompact, Selection: selection, Band: band}, width, height)
	}
	return m.promptBody(painter, width, height)
}

// menuKeys names the docked menu's controls with the shared key vocabulary.
func menuKeys(p *sessionpresentation.MenuView, width int) string {
	if strings.HasPrefix(string(p.Kind), "preset") {
		return presetKeys(p, width)
	}
	keys := "enter select · esc cancel"
	switch p.Kind {
	case sessionpresentation.MenuSettings:
		keys = "enter apply · esc cancel"
	case sessionpresentation.MenuCopy:
		keys = "enter copy · esc close"
	}
	// The arrow hint yields first when the keys would otherwise wrap.
	if arrows := "↑/↓ choose · " + keys; ansi.StringWidth(arrows) <= width-4 {
		return arrows
	}
	return keys
}

// dockFooter wraps dim dock keys rather than truncating them, keeping each
// key with its verb.
func dockFooter(keys string, width int) []string {
	width = max(1, width-4)
	var lines []string
	line := ""
	for _, hint := range strings.Split(keys, " · ") {
		if line != "" && ansi.StringWidth(line+" · "+hint) <= width {
			line += " · " + hint
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		line = hint
	}
	return append(lines, panelWrap(line, width)...)
}

// dockHeading paints a docked menu's accent heading with its close control.
func (m *Menu) dockHeading(painter *terminalui.Painter, title string, width int) string {
	heading := terminalui.PanelHeading(painter.Theme.Accent(), title, "", terminalui.DialogClose, max(1, width-4))
	m.closeShown = strings.Contains(ansi.Strip(heading), terminalui.DialogClose)
	return "  " + heading
}

// menuRows publishes each choice's state and risk to the shared list.
func menuRows(p *sessionpresentation.MenuView) []terminalui.ChoiceRow {
	rows := make([]terminalui.ChoiceRow, len(p.Choices))
	for i, choice := range p.Choices {
		rows[i] = choiceRow(choice, choice.Label())
	}
	return rows
}

func choiceRow(choice completion.Choice, text string) terminalui.ChoiceRow {
	var states []string
	if choice.Current {
		states = append(states, "current")
	}
	if choice.Unavailable {
		states = append(states, "unavailable")
	}
	return terminalui.ChoiceRow{Text: text, Description: choice.Description, Status: strings.Join(states, " · "), Danger: choice.Danger, Muted: choice.Unavailable}
}

// promptBody docks a prompt as a panel matching the process list: an accent
// heading with its close control, the dim explanation, choices with a pointer
// on the selected one, and dim keys, inset two cells. Short docks keep the
// choices first.
func (m *Menu) promptBody(painter *terminalui.Painter, width, height int) []string {
	p := m.body
	accent := painter.Theme.Accent()
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	title := p.Title
	if p.Kind == sessionpresentation.MenuSettings {
		title = "Choose " + strings.TrimPrefix(p.Setting, "/")
	} else if p.Kind == sessionpresentation.MenuCopy {
		title = "Copy to clipboard"
	}
	first, last := 0, height
	if height >= 3 {
		put(0, m.dockHeading(painter, title, width))
		footer := dockFooter(menuKeys(p, width), width)
		footer = footer[:min(len(footer), height-2)]
		for i, line := range footer {
			put(height-len(footer)+i, "  "+terminalui.Dim+line+terminalui.Reset)
		}
		first, last = 1, height-len(footer)
		if height-len(footer) >= 4 {
			first, last = 2, last-1
		}
	}
	if first == 2 {
		if explanation := promptExplanation(p, width); len(explanation) > 0 {
			for _, line := range explanation {
				if first >= last-2 {
					break
				}
				put(first, "    "+terminalui.Dim+line+terminalui.Reset)
				first++
			}
			first++
		}
	}
	visible := min(8, max(0, last-first))
	// The list's problem row is the spacer above the keys.
	list := make([]string, min(height, last+2))
	m.ChoiceListLayout.Draw(list, max(1, width-4), first, visible, terminalui.ChoiceListView{Rows: menuRows(p), Selected: m.Selected, Marker: accent, Danger: painter.Theme.Danger(), Problem: p.Problem, Loading: p.Loading})
	for y, row := range list {
		if row != "" {
			put(y, "  "+row)
		}
	}
	return frame
}

// promptExplanation wraps an inline prompt's explanation for its panel.
func promptExplanation(p *sessionpresentation.MenuView, width int) []string {
	if !inlinePrompt(p.Kind) || strings.TrimSpace(p.Explanation) == "" {
		return nil
	}
	return panelWrap(p.Explanation, max(1, width-6))
}

// inlinePrompt reports menus that ask one titled question above the composer.
func inlinePrompt(kind sessionpresentation.MenuKind) bool {
	switch kind {
	case sessionpresentation.MenuArchive, sessionpresentation.MenuRewind, sessionpresentation.MenuRewindEdits,
		sessionpresentation.MenuSideReplace, sessionpresentation.MenuExit:
		return true
	}
	return false
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
	ph := min(height, m.Height(width))
	frame := make([]string, height)
	if height > ph {
		copy(frame, view.Render(width, height-ph, now))
	}
	copy(frame[height-ph:], m.Render(&view.Painter.Painter, SelectionStyle(&view.Painter.Painter), width, ph))
	m.Rect = Rect{0, height - ph, width, ph}
	return frame
}

// presetListWidth bounds the choice column so long presets truncate and the
// selection band stays beside the list instead of spanning wide terminals.
const presetListWidth = 64

// presetEditorCopy replaces the shared Edit explanation, which describes the
// browser's inline editing.
const presetEditorCopy = "Write a prompt or slash command in your external editor, then save and close it."

// renderPreset paints the dock heading, context, keyed choices, a problem
// row and keys. Context yields to choices when the terminal is short; a list
// that still scrolls gains a spacer for its upper marker.
func (m *Menu) renderPreset(painter *terminalui.Painter, width, height int) []string {
	p := m.body
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	rows := presetRows(p)
	listWidth := max(1, width-4)
	if len(rows) > 0 {
		widest := 0
		for _, row := range rows {
			widest = max(widest, ansi.StringWidth(panelText(row.Text)))
		}
		listWidth = min(listWidth, presetListWidth, widest+4) // Pointer prefix and a trailing margin.
	}
	view := terminalui.ChoiceListView{Rows: rows, Selected: m.Selected, Marker: painter.Theme.Accent(), Danger: painter.Theme.Danger(), Loading: p.Loading}
	if len(rows) == 0 {
		view.Problem = p.Problem
	}
	draw := func(first, visible, size int) {
		list := make([]string, size)
		m.ChoiceListLayout.Draw(list, listWidth, first, visible, view)
		for y, row := range list {
			if row != "" {
				put(y, "  "+row)
			}
		}
	}
	footer := dockFooter(presetKeys(p, width), width)
	if height < len(footer)+5 {
		draw(0, height, height)
		return frame
	}
	put(0, m.dockHeading(painter, p.Title, width))
	for i, line := range footer {
		put(height-len(footer)+i, "  "+terminalui.Dim+line+terminalui.Reset)
	}
	bottom := height - len(footer) - 1 // The problem row.
	list, room := max(1, len(p.Choices)), bottom-2
	// Context aligns with the choice labels, inside their pointer column.
	header := presetHeader(p, width-2)
	header = header[:min(len(header), max(0, room-list))]
	for i, line := range header {
		put(2+i, "  "+line)
	}
	first := 2 + len(header)
	if list > room {
		first++
	}
	draw(first, bottom-first, bottom+2)
	if p.Problem != "" && len(rows) > 0 {
		put(bottom, "  "+painter.Theme.Danger()+panelText(p.Problem)+terminalui.Reset) // Problems may be wider than the list.
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
		rows[i] = choiceRow(choice, label)
	}
	return rows
}

// presetKeys names the controls each stage accepts, matching Menu.Key.
func presetKeys(p *sessionpresentation.MenuView, width int) string {
	switch {
	case p.Loading:
		return "esc close"
	case len(p.Choices) == 1 && p.Choices[0].Name == "retry":
		return "r retry · esc close"
	case p.Kind == "preset-edit":
		return "enter open editor · esc cancel"
	case p.Kind == "preset-label" && len(p.Choices) > 0:
		return "enter " + strings.ToLower(p.Choices[0].Label()) + " · esc cancel"
	case p.Kind == "preset-confirm":
		return "y confirm · esc cancel"
	case width < 50:
		return "enter select · esc close"
	}
	return "↑/↓ choose · enter select · esc close"
}
