package terminalui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
)

// Source: modules/activity/terminalview/paint.go:1043:1062@563f0c37.
// TextSelection replaces backgrounds while retaining syntax colors. Clear
// reverse-video carets and restore the fill after resets and code/diff fills.
func TextSelection(text string, theme diffrender.Theme) string {
	fill := theme.SelectionBackground() + "\x1b[27m"
	var out strings.Builder
	out.WriteString(fill)
	var state byte
	for text != "" {
		seq, _, n, next := ansi.DecodeSequence(text, state, nil)
		out.WriteString(seq)
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			out.WriteString(fill)
		}
		text, state = text[n:], next
	}
	out.WriteString("\x1b[49m")
	return out.String()
}
