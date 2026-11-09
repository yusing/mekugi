// Package terminal fits path labels to terminal-cell budgets.
package terminal

import "github.com/charmbracelet/x/ansi"

// Truncate keeps the distinctive suffix of a display path within width terminal
// cells. It never changes the operational path; callers apply it only at paint.
func Truncate(path string, width int) string {
	if width <= 0 {
		return ""
	}
	cells := ansi.StringWidth(path)
	if cells <= width {
		return path
	}
	if width == 1 {
		return "…"
	}
	cut := cells - width + 1
	result := ansi.TruncateLeft(path, cut, "…")
	// A cut through a wide grapheme keeps that whole grapheme. Move the cut
	// past it rather than overrunning the caller's cell budget.
	for ansi.StringWidth(result) > width {
		cut++
		result = ansi.TruncateLeft(path, cut, "…")
	}
	if ansi.StringWidth(result) == 0 {
		return "…"
	}
	return result
}

// TruncateMove keeps both rename endpoints distinct when they fit beside the
// arrow. Tiny labels keep the destination; the underlying move stays unchanged.
func TruncateMove(before, after string, width int) string {
	if width < 5 {
		return Truncate(after, width)
	}
	if ansi.StringWidth(before+" → "+after) <= width {
		return before + " → " + after
	}
	before = Truncate(before, (width-3)/2)
	return before + " → " + Truncate(after, width-3-ansi.StringWidth(before))
}
