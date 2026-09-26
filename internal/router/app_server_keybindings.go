package router

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func renderNativeKeybindings(width, height int) []string {
	const bold = "\x1b[1m"
	const blue = "\x1b[38;2;80;155;225m"
	groups := []struct {
		title string
		keys  [][2]string
	}{
		{"Compose", [][2]string{
			{"ctrl+j", "New line"},
			{"arrows", "Move caret"},
			{"ctrl+← / ctrl+→", "Previous / next word"},
			{"ctrl+↑ / ctrl+↓", "Start / end of line"},
			{"alt+backspace / del", "Delete word"},
			{"ctrl+z / ctrl+y", "Undo / redo"},
			{"ctrl+v", "Paste image"},
			{"ctrl+g", "External editor"},
		}},
		{"Session", [][2]string{
			{"enter", "Send / steer"},
			{"ctrl+c", "Clear / interrupt / quit"},
			{"ctrl+b 1–4", "Focus pane"},
			{"ctrl+b e", "Next live item"},
			{"ctrl+b ← / →", "Resize panes"},
		}},
		{"Transcript", [][2]string{
			{"pgup / pgdn", "Scroll"},
		}},
	}
	lines := []string{"", "  " + bold + "Keyboard shortcuts" + liveActivityReset, ""}
	var band []string
	used := 0
	for _, group := range groups {
		keyWidth := 0
		for _, key := range group.keys {
			keyWidth = max(keyWidth, ansi.StringWidth(key[0]))
		}
		column := []string{bold + group.title + liveActivityReset}
		columnWidth := ansi.StringWidth(group.title)
		for _, key := range group.keys {
			line := blue + key[0] + liveActivityReset + strings.Repeat(" ", keyWidth-ansi.StringWidth(key[0])+2) + key[1]
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
	lines = append(lines, "", "  "+blue+"? / esc"+liveActivityReset+"  Close shortcuts")
	frame := make([]string, height)
	start := max(0, height-len(lines))
	for i := range min(height, len(lines)) {
		frame[start+i] = ansi.Truncate(lines[i], width, "")
	}
	return frame
}
