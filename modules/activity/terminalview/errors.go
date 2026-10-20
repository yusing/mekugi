package terminalview

import (
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/terminalui"
)

// ErrorPreview bounds inline diagnostics independently of the full dialog body.
// A single first line also keeps multiline host errors out of the transcript.
func ErrorPreview(body string) string {
	const previewBytes = 240
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(textsanitize.Safe(line, false))
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

// ErrorHasDetails reports content omitted by the inline first-line preview.
func ErrorHasDetails(block Block) bool {
	return strings.TrimSpace(textsanitize.Safe(block.Body, false)) != ErrorPreview(block.Body)
}

// ErrorRows adds a dialog hint only when the preview omits retained content.
func ErrorRows(block Block, width int) []string {
	rows := terminalui.Wrap(terminalui.Red+ErrorPreview(block.Body)+terminalui.Reset, width, false)
	if !ErrorHasDetails(block) {
		return rows
	}
	hint := "details · ctrl+b !"
	if block.Hovered {
		hint = Underline(hint)
	}
	return append(rows, terminalui.Wrap(terminalui.Dim+hint+terminalui.Undim, width, false)...)
}
