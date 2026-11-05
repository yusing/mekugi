package presentation

import (
	"github.com/yusing/mekugi/libraries/textsanitize"
	"strings"
)

// Source: modules/activity/terminalview/errors.go:11:26@f7b0377da.
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
