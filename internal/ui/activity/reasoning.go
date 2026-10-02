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

// ReasoningSummaryBody removes a detected section title only when paragraphs
// follow it. Heading-only summaries remain public content, not empty bodies.
func ReasoningSummaryBody(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(strings.TrimSuffix(text, "<!-- -->"))
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && reasoningTitle(lines, 0) != "" {
		if body := strings.TrimSpace(strings.Join(lines[1:], "\n")); body != "" {
			return body
		}
	}
	return text
}

// ThinkingTailRows is how much reasoning stays visible while it
// streams, following grok-build's truncated thinking blocks.
const ThinkingTailRows = 3

// thinkingHeader labels long reasoning blocks; short summaries render directly.
func (p *Painter) thinkingHeader(block Block, width int) (string, bool) {
	label := block.Label
	if label == "" {
		label = ReasoningSections(block.Body)[0].Label
	}
	if label == "" && block.Live {
		label = "Thinking…"
	} else if label == "" {
		label = "Thought"
	}
	label = p.Inline(label)
	if !block.Live && block.Elapsed != "" {
		suffix := " for " + block.Elapsed
		label = strings.TrimSuffix(reasoningFor(label, block.Elapsed), suffix)
		if width > ansi.StringWidth(suffix) {
			room := width - ansi.StringWidth(suffix)
			return ansi.Truncate(label, room, "…") + suffix, ansi.StringWidth(label) > room
		}
		label += suffix
	}
	return ansi.Truncate(label, max(1, width), "…"), ansi.StringWidth(label) > max(1, width)
}

// Strip the visible trailing period, preserving Markdown's terminal styles.
func reasoningFor(text, elapsed string) string {
	if dot := strings.LastIndexByte(text, '.'); dot >= 0 && ansi.Strip(text[dot+1:]) == "" {
		text = text[:dot] + text[dot+1:]
	}
	return text + " for " + elapsed
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

// ReasoningElided uses the same wrapped body and short-summary rule as the
// renderer. A settled one-line summary is not a collapsed detail view.
func (p *Painter) ReasoningElided(block Block, width int) bool {
	width = max(8, width)
	rows, short, titleOnly := p.reasoningContent(block, width)
	_, headingElided := p.thinkingHeader(block, width-2)
	return len(rows) > 0 && !short && (block.Collapsed || block.Live && len(rows) > ThinkingTailRows || headingElided && !titleOnly)
}

func (p *Painter) reasoningContent(block Block, width int) (rows []string, short, titleOnly bool) {
	if block.Label == "" {
		block.Label = ReasoningSections(block.Body)[0].Label
	}
	body := ReasoningSummaryBody(block.Body)
	if body == "" {
		return nil, false, false
	}
	rows = p.markdown(body, max(1, width-2), true)
	titleOnly = body == strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(block.Body), "<!-- -->"))
	return rows, len(rows) == 1 && (block.Label == "" || titleOnly), titleOnly
}
