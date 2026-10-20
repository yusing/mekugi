// Source: apps/mekugi/internal/router/app_server_keybindings.go:10:108@[ce8700a8].
package terminalsession

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
)

func Keybindings(width, height int) []string {
	const bold = "\x1b[1m"
	const blue = "\x1b[38;2;80;155;225m"
	groups := []struct {
		title string
		keys  [][2]string
	}{
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
			{"ctrl+b e", "Next live item"},
			{"ctrl+b q / !", "Questions / error details"},
			{"ctrl+b ← / →", "Resize panes"},
		}},
		{"Transcript", [][2]string{
			{"ctrl+e", "Default / events / all"},
			{"pgup / pgdn", "Scroll"},
			{"esc", "Back to bottom"},
			{"ctrl+b o", "Older child history"},
		}},
	}
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
		for _, key := range group.keys {
			line := blue + key[0] + terminalui.Reset + strings.Repeat(" ", keyWidth-ansi.StringWidth(key[0])+2) + key[1]
			column = append(column, line)
			columnWidth = max(columnWidth, ansi.StringWidth(line))
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
	lines = append(lines, "", "  "+blue+"? / esc"+terminalui.Reset+"  Close shortcuts")
	// Give controls priority over spacing when an extra binding fills a narrow frame.
	for i := len(lines) - 2; len(lines) > height && i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			lines = append(lines[:i], lines[i+1:]...)
		}
	}
	frame := make([]string, height)
	start := max(0, height-len(lines))
	for i := range min(height, len(lines)) {
		frame[start+i] = ansi.Truncate(lines[i], width, "")
	}
	return frame
}
