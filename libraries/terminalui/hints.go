package terminalui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Hint names one terminal action and its visible key label.
// Source: apps/mekugi/internal/router/terminal_hints.go:9:38@563f0c37.
type Hint struct {
	Key, Label string
	Action     byte
}

// Hints share drawing and complete-label hit regions.
type Hints []Hint

func (h Hints) Render() string {
	parts := make([]string, len(h))
	for i, hint := range h {
		parts[i] = "\x1b[1m" + hint.Key + "\x1b[22m " + hint.Label
	}
	return strings.Join(parts, " · ")
}

// Hit regions use the same labels and separators as painting; clipped labels
// and separators are not actions.
func (h Hints) ActionAt(x, width int) byte {
	left := 0
	for _, hint := range h {
		right := left + ansi.StringWidth(hint.Key+" "+hint.Label)
		if x >= left && x < right && right <= width {
			return hint.Action
		}
		left = right + 3
	}
	return 0
}
