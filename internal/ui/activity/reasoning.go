package activity

import (
	"slices"
	"strings"
)

// Source: codex-rs/tui/src/chatwidget/streaming.rs:9:24@86be5320 latest_summary_line.
func ReasoningSummaryHeader(text string) string {
	for _, line := range slices.Backward(strings.Split(text, "\n")) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<!--") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if rest, ok := strings.CutPrefix(line, "**"); ok {
			bold, trailing, closed := strings.Cut(rest, "**")
			if !closed {
				continue
			}
			line = bold + trailing
		}
		if line != "" {
			return line
		}
	}
	return "Thinking"
}

// Source: codex-rs/tui/src/history_cell/messages.rs:749:798@86be5320
// split_reasoning_summary_parts, for the collector's single accumulated item.
func ReasoningSummaryBody(text string) string {
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, "**"); ok {
		if _, body, closed := strings.Cut(rest, "**"); closed {
			if strings.TrimSpace(body) == "<!-- -->" {
				return ""
			}
			if strings.HasPrefix(body, "\n") || strings.HasPrefix(body, "\r") {
				text = strings.TrimSpace(body)
			}
		}
	}
	if text == "<!-- -->" {
		return ""
	}
	return text
}
