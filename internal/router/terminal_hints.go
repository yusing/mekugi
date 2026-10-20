package router

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type terminalHint struct {
	key, label string
	action     byte
}

type terminalHints []terminalHint

var selectionHints = terminalHints{{"r", "reference", 'r'}, {"ctrl+c", "copy", 'c'}, {"esc", "clear", 27}}

func (h terminalHints) render() string {
	parts := make([]string, len(h))
	for i, hint := range h {
		parts[i] = "\x1b[1m" + hint.key + "\x1b[22m " + hint.label
	}
	return strings.Join(parts, " · ")
}

// Hit regions use the same labels and separators as painting; clipped labels
// and separators are not actions.
func (h terminalHints) actionAt(x, width int) byte {
	left := 0
	for _, hint := range h {
		right := left + ansi.StringWidth(hint.key+" "+hint.label)
		if x >= left && x < right && right <= width {
			return hint.action
		}
		left = right + 3
	}
	return 0
}
