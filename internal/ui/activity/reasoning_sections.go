package activity

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// ReasoningSections keeps titled summaries independently readable and
// reopenable, retaining each section's original Markdown for the dialog.
func ReasoningSections(text string) []Block {
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	var blocks []Block
	start, label, fence := 0, "", ""
	appendSection := func(end int) {
		body := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if body != "" {
			blocks = append(blocks, Block{Kind: "summary", Section: len(blocks), Label: label, Body: body})
		}
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		mark, delimiter := FenceDelimiter(trimmed)
		if !delimiter && strings.HasPrefix(trimmed, "~~~") {
			mark, delimiter = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "~"))], true
		}
		if delimiter {
			if fence == "" {
				fence = mark
			} else if strings.HasPrefix(mark, fence) && strings.TrimSpace(trimmed[len(mark):]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
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
		return []Block{{Kind: "summary", Body: text}}
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
		i > 0 && strings.TrimSpace(lines[i-1]) != "" || ansi.StringWidth(line) > 60 || len(strings.Fields(line)) > 8 ||
		strings.ContainsAny(line[len(line)-1:], ".?!:;") || strings.ContainsAny(line[:1], "-*+>|`~") || unicode.IsDigit(rune(line[0])) {
		return ""
	}
	return line
}
