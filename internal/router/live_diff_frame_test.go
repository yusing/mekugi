package router

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The UI harness consumes complete terminal frames, not publication callbacks.
// It runs the real viewer process against the authenticated router event stream.

func liveDiffFrameRow(frame string, row int) string {
	return ansi.Strip(liveDiffFrameRawRow(frame, row))
}

func liveDiffFrameRawRow(frame string, row int) string {
	start := strings.LastIndex(frame, fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K", row))
	if start < 0 {
		return ""
	}
	text := frame[start:]
	text, _, _ = strings.Cut(text, fmt.Sprintf("\x1b[%d;1H", row+1))
	return text
}
