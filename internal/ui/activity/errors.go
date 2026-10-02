package activity

import (
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
)

// ErrorPreview bounds inline diagnostics independently of the full dialog body.
// A single first line also keeps multiline host errors out of the transcript.
func ErrorPreview(body string) string {
	const previewBytes = 240
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(livediff.Safe(line, false))
		if line == "" {
			continue
		}
		if len(line) > previewBytes {
			return strings.ToValidUTF8(line[:previewBytes], "") + "…"
		}
		return line
	}
	return ""
}

// ErrorRows paints only the preview and a discoverable shared-dialog target.
func ErrorRows(block Block, width int) []string {
	rows := Wrap(Red+ErrorPreview(block.Body)+Reset, width, false)
	hint := "details · ctrl+b !"
	if block.Hovered {
		hint = Underline(hint)
	}
	return append(rows, Wrap(Dim+hint+Undim, width, false)...)
}
