// Source: apps/mekugi/internal/router/app_server_keybindings.go:10:108@[ce8700a8].
package terminalsession

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Keybindings lists shortcuts in groups, side by side when they fit. A group
// narrower than its descriptions wraps them under their own column when the
// frame has room, and otherwise ends them with an ellipsis.
func Keybindings(theme diffrender.Theme, width, height int) []string {
	accent := theme.Accent()
	groups := []keybindingGroup{
		{"Compose", [][2]string{
			{"!", "Shell Mode"},
			{"ctrl+j", "New line"},
			{"/ / @ / $", "Pick command / file / skill"},
			{"arrows", "Move caret / input history"},
			{"ctrl+← / ctrl+→", "Previous / next word"},
			{"alt/option+← / →", "Previous / next word"},
			{"ctrl+↑ / ctrl+↓", "Start / end of line"},
			{"alt/option+backspace", "Delete previous word"},
			{"ctrl+w / alt+del", "Delete previous / next word"},
			{"ctrl+k", "Delete to end of line"},
			{"ctrl+z / ctrl+y", "Undo / redo"},
			{"ctrl+v", "Paste image"},
			{"ctrl+g", "External editor"},
		}},
		{"Session", [][2]string{
			{"enter", "Send / steer"},
			{"tab", "Queue for next turn"},
			{"alt+↑ / shift+←", "Edit last queued"},
			{"shift+↑ / shift+↓", "Raise / lower reasoning"},
			{"/status /session", "Settings / launch metrics"},
			{"/ps", "Running processes"},
			{"/copy", "Copy response or part of it"},
			{"/btw QUESTION", "Side question / follow-up"},
			{"/model /effort /tier", "Open picker or set VALUE"},
			{"ctrl+c", "Clear / interrupt / quit"},
			{"esc", "Interrupt, keep draft"},
			{"ctrl+b 1–3", "Main / toggle Diff / Agents"},
			{"ctrl+b e / r", "Pin next live edit / follow live"},
			{"ctrl+b q", "Approvals, then questions"},
			{"ctrl+b !", "Error details"},
			{"ctrl+b p", "Presets"},
			{"ctrl+b ← / →", "Resize panes"},
		}},
		{"Transcript", [][2]string{
			{"ctrl+e", "Default / events / all"},
			{"pgup / pgdn", "Scroll"},
			{"esc", "Back to bottom"},
		}},
	}
	// Wrapped descriptions keep whole words; a frame too short for them
	// truncates descriptions with an ellipsis instead.
	lines := keybindingLines(groups, accent, width, height, true)
	if len(lines) > height {
		lines = keybindingLines(groups, accent, width, height, false)
	}
	frame := make([]string, height)
	start := max(0, height-len(lines))
	for i := range min(height, len(lines)) {
		frame[start+i] = ansi.Truncate(lines[i], width, "…")
	}
	return frame
}

type keybindingGroup struct {
	title string
	keys  [][2]string
}

func keybindingLines(groups []keybindingGroup, accent string, width, height int, wrap bool) []string {
	const bold = "\x1b[1m"
	lines := []string{"", "  " + bold + "Keyboard shortcuts" + terminalui.Reset, ""}
	var band []string
	used := 0
	for _, group := range groups {
		keyWidth := 0
		for _, key := range group.keys {
			keyWidth = max(keyWidth, ansi.StringWidth(key[0]))
		}
		column := []string{bold + group.title + terminalui.Reset}
		columnWidth := ansi.StringWidth(group.title)
		described := max(12, width-2-keyWidth-2)
		for _, key := range group.keys {
			parts := []string{key[1]}
			if wrap {
				parts = panelWrap(key[1], described)
			}
			for i, part := range parts {
				line := strings.Repeat(" ", keyWidth+2) + part
				if i == 0 {
					line = accent + key[0] + terminalui.Reset + strings.Repeat(" ", keyWidth-ansi.StringWidth(key[0])+2) + part
				}
				column = append(column, line)
				columnWidth = max(columnWidth, ansi.StringWidth(line))
			}
		}
		if used > 0 && 2+used+4+columnWidth > width {
			lines = append(lines, band...)
			lines = append(lines, "")
			band, used = nil, 0
		}
		for len(band) < len(column) {
			band = append(band, "")
		}
		for row := range band {
			start := 2
			if used > 0 {
				start += used + 4
			}
			band[row] += strings.Repeat(" ", start-ansi.StringWidth(band[row]))
			if row < len(column) {
				band[row] += column[row]
			}
		}
		if used > 0 {
			used += 4
		}
		used += columnWidth
	}
	lines = append(lines, band...)
	lines = append(lines, "", "  "+accent+"? / esc"+terminalui.Reset+"  Close shortcuts")
	// Give controls priority over spacing when an extra binding fills a narrow frame.
	for i := len(lines) - 2; len(lines) > height && i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			lines = append(lines[:i], lines[i+1:]...)
		}
	}
	return lines
}
