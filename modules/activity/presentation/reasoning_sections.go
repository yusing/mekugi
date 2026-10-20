package presentation

import (
	"slices"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// Source: modules/activity/terminalview/reasoning_sections.go:11:84@35fd8de4.
// ReasoningSections keeps titled summaries independently readable and
// reopenable, retaining each section's original Markdown for the dialog.
func ReasoningSections(text string) []Value {
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	var blocks []Value
	start, label, fence := 0, "", ""
	appendSection := func(end int) {
		body := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if body != "" {
			blocks = append(blocks, Value{Kind: "summary", Section: len(blocks), Label: label, Body: body})
		}
	}
	for i, line := range lines {
		if fence != "" {
			if markdownsyntax.ClosesFence(line, fence) {
				fence = ""
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		mark, delimiter := markdownsyntax.FenceDelimiter(trimmed)
		if !delimiter && strings.HasPrefix(trimmed, "~~~") {
			mark, delimiter = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "~"))], true
		}
		if delimiter {
			fence = mark
			continue
		}
		title := reasoningTitle(lines, i)
		if title == "" {
			continue
		}
		appendSection(i)
		start, label = i, title
	}
	appendSection(len(lines))
	if len(blocks) == 0 {
		return []Value{{Kind: "summary", Body: text}}
	}
	return blocks
}

func reasoningTitle(lines []string, i int) string {
	line := strings.TrimSpace(lines[i])
	if line == "" || strings.HasPrefix(lines[i], "\t") || len(lines[i])-len(strings.TrimLeft(lines[i], " \t")) >= 4 {
		return ""
	}
	if rest, ok := strings.CutPrefix(line, "**"); ok {
		if title, trailing, closed := strings.Cut(rest, "**"); closed && strings.TrimSpace(trailing) == "" {
			return strings.TrimSpace(title)
		}
		return ""
	}
	if strings.HasPrefix(line, "#") {
		rest := strings.TrimLeft(line, "#")
		if len(line)-len(rest) <= 6 && strings.HasPrefix(rest, " ") {
			title := strings.TrimSpace(rest)
			if before, closing, ok := strings.CutLast(title, " "); ok && strings.Trim(closing, "#") == "" {
				title = strings.TrimSpace(before)
			}
			return title
		}
		return ""
	}
	// Plain titles must be short standalone lines, not sentences or Markdown
	// list/table/code content. Explicit headings do not need this heuristic.
	if i+2 >= len(lines) || strings.TrimSpace(lines[i+1]) != "" || strings.TrimSpace(lines[i+2]) == "" ||
		i > 0 && strings.TrimSpace(lines[i-1]) != "" || uniseg.StringWidth(line) > 60 || len(strings.Fields(line)) > 8 ||
		strings.ContainsAny(line[len(line)-1:], ".?!:;") || strings.ContainsAny(line[:1], "-*+>|`~") || unicode.IsDigit(rune(line[0])) {
		return ""
	}
	return line
}

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
