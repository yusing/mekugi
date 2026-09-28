package activity

import (
	"slices"
	"strings"
	"time"
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

// ReasoningTitled reports a Codex summary, which opens with a bold heading.
// Third-party providers stream untitled plaintext reasoning instead.
// A heading still streaming counts, so the block never flips style.
func ReasoningTitled(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "**")
}

// ThinkingTailRows is how much of untitled reasoning stays visible while it
// streams, following grok-build's truncated thinking blocks.
const ThinkingTailRows = 3

// ThinkingFoldDelay is how long a finished thinking block stays open.
const ThinkingFoldDelay = time.Second

// ThinkingFolds reports untitled reasoning, observed finishing live, that
// should now collapse to its header. History keeps its body.
func ThinkingFolds(block Block, now time.Time) bool {
	return block.Kind == "summary" && !block.Live && !block.Done.IsZero() && !ReasoningTitled(block.Body) &&
		now.Sub(block.Done) >= ThinkingFoldDelay
}

// ThinkingHeader labels untitled reasoning the way grok-build does.
func ThinkingHeader(live bool, elapsed string) string {
	switch {
	case live:
		return "Thinking…"
	case elapsed != "":
		return "Thought for " + elapsed
	}
	return "Thought"
}
