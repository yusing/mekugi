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

// ReasoningTitled reports a Codex summary, which opens with a bold heading.
// Third-party providers stream untitled plaintext reasoning instead.
// A heading still streaming counts, so the block never flips style.
func ReasoningTitled(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "**")
}

// ThinkingTailRows is how much of untitled reasoning stays visible while it
// streams, following grok-build's truncated thinking blocks.
const ThinkingTailRows = 3

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

// GroupReasoning folds earlier summaries in each uninterrupted run into one
// expandable row. The latest summary stays expanded.
func GroupReasoning(blocks []Block) []Block {
	var out []Block
	for i := 0; i < len(blocks); {
		end := i + 1
		if blocks[i].Kind == "summary" {
			for end < len(blocks) && blocks[end].Kind == "summary" {
				end++
			}
		}
		if end-i > 1 {
			members := slices.Clone(blocks[i : end-1])
			headers := make([]string, 0, len(members))
			for _, member := range members {
				headers = append(headers, ReasoningSummaryHeader(member.Body))
			}
			out = append(out, Block{Kind: "summary", Source: blocks[i].Source, Label: strings.Join(headers, ", "), Members: members, Collapsed: true})
			latest := blocks[end-1]
			latest.Collapsed = false
			out = append(out, latest)
		} else {
			out = append(out, blocks[i])
		}
		i = end
	}
	return out
}
