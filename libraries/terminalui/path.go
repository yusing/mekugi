package terminalui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Source: internal/ui/activity/paint.go:82:87@[8a494004]
// StyledPath emphasizes the filename in an already resolved display path.
func StyledPath(path string) string {
	if i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/"); i >= 0 {
		return Dim + path[:i+1] + Undim + "\x1b[1m" + path[i+1:] + Undim
	}
	return "\x1b[1m" + path + Undim
}

// Source: modules/activity/terminalview/paint.go:650:684@563f0c37.
// TruncatePath uses the feed's path elision in single-line controls, including
// widths where feed rows would wrap instead. It keeps nearby directories and
// the filename, or the filename's start and extension when it cannot fit.
func TruncatePath(path string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(path) <= width {
		return path
	}
	i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/")
	base := path[i+1:]
	if i >= 0 && ansi.StringWidth(base)+2 <= width {
		// The last separator always fits, so this finds a cut.
		for cut := 1; cut <= i; cut++ {
			if path[cut] == '/' && 1+ansi.StringWidth(path[cut:]) <= width {
				return "…" + path[cut:]
			}
		}
	}
	// The name alone is too wide: keep its start and its extension.
	if i >= 0 && width > 4 {
		return "…/" + elideMiddle(base, width-2)
	}
	return elideMiddle(path, width)
}

func elideMiddle(text string, width int) string {
	total := ansi.StringWidth(text)
	if total <= width {
		return text
	}
	tail := (width - 1) / 2
	return ansi.Truncate(text, width-1-tail, "") + "…" + ansi.TruncateLeft(text, total-tail, "")
}
