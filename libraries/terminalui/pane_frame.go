package terminalui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Source: apps/mekugi/internal/router/native_shell.go:198:267@[da8d8dc9].
func PaneBorder(focused bool) string {
	if focused {
		return "\x1b[38;2;130;170;255m"
	}
	return "\x1b[38;2;70;70;90m"
}

// PaneRule is a pane edge with embedded labels. The right label yields
// first when both do not fit.
func PaneRule(open, close, fill, left, right string, width int, color string) string {
	if width < 2 {
		return ""
	}
	inner := width - 2
	segment := func(label string) string {
		if label == "" {
			return ""
		}
		return " \x1b[39m" + label + Reset + color + " "
	}
	l, r := segment(left), segment(right)
	if ansi.StringWidth(l)+ansi.StringWidth(r)+1 > inner {
		r = ""
	}
	if ansi.StringWidth(l)+1 > inner {
		l = ansi.Truncate(l, max(0, inner-2), "…") + Reset + color + " "
	}
	gap := max(0, inner-ansi.StringWidth(l)-ansi.StringWidth(r))
	return color + open + l + strings.Repeat(fill, gap) + r + close + Reset
}

// PaneBox frames a pane. rules replace whole rows, borders included, so a
// dock separator can span the frame.
func PaneBox(width, height int, title, right string, focused bool, body []string, rules map[int]string) []string {
	if width < 4 || height < 3 {
		return nil
	}
	color := PaneBorder(focused)
	lines := []string{PaneRule("┌", "┐", "─", title, right, width, color)}
	for row := range height - 2 {
		if rule, ok := rules[row]; ok {
			lines = append(lines, rule)
			continue
		}
		text := ""
		textWidth := 0
		if row < len(body) {
			text = body[row]
			textWidth = ansi.StringWidth(text)
			if textWidth > width-2 {
				text = ansi.Truncate(text, width-2, "")
				textWidth = ansi.StringWidth(text)
			}
		}
		lines = append(lines, color+"│"+Reset+text+strings.Repeat(" ", max(0, width-2-textWidth))+color+"│"+Reset)
	}
	return append(lines, color+"└"+strings.Repeat("─", width-2)+"┘"+Reset)
}

// PaneTitle labels a pane with its Ctrl-B digit.
func PaneTitle(digit int, name, detail string, focused bool) string {
	label := fmt.Sprintf("%d %s", digit, name)
	if focused {
		label = PaneBorder(true) + "\x1b[1m" + label + "\x1b[22m"
	} else {
		label = Dim + label + Undim
	}
	if detail != "" {
		label += Dim + " · " + Undim + detail
	}
	return label
}
