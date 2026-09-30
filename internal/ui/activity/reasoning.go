package activity

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
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

// ThinkingTailRows is how much reasoning stays visible while it
// streams, following grok-build's truncated thinking blocks.
const ThinkingTailRows = 3

// ThinkingHeader labels reasoning the way grok-build does.
func ThinkingHeader(live bool, elapsed string) string {
	switch {
	case live:
		return "Thinking…"
	case elapsed != "":
		return "Thought for " + elapsed
	}
	return "Thought"
}

// reasoningRow keeps the whole row faint and italic, including text after
// Markdown's combined style resets. Reapply only these attributes, preserving
// inline colors, hyperlinks, and the remaining Markdown styles.
func reasoningRow(row string) string {
	const style = Dim + "\x1b[3m"
	var out strings.Builder
	out.WriteString(style)
	var state byte
	for len(row) > 0 {
		seq, _, n, next := ansi.DecodeSequence(row, state, nil)
		if n == 0 {
			break
		}
		state, row = next, row[n:]
		out.WriteString(seq)
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			out.WriteString(style)
		}
	}
	out.WriteString(Reset)
	return out.String()
}
