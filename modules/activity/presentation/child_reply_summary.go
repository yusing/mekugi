package presentation

import (
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// replySummary interprets only completed child replies. Its result is detached
// presentation data on the same identified value as the unmodified source body.
func (entry ItemContent) replySummary(body string) string {
	if !entry.ItemComplete || entry.Agent == "" || entry.Agent == "/root" || entry.Agent == "Main" {
		return ""
	}
	if entry.Message != nil && entry.Message.To != "/root" && entry.Message.To != "Main" {
		return ""
	}
	lines := strings.Split(body, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start == len(lines) || strings.TrimSuffix(lines[start], "\r") != "## Summary" {
		return ""
	}
	start++
	fence := ""
	for i := start; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if fence != "" {
			if markdownsyntax.ClosesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if mark, ok := markdownsyntax.FenceDelimiter(line); ok {
			fence = mark
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "~"))]
			continue
		}
		switch line {
		case "## Summary":
			return ""
		case "## Details":
			summary := strings.Join(lines[start:i], "\n")
			if strings.TrimSpace(summary) != "" && strings.TrimSpace(strings.Join(lines[i+1:], "\n")) != "" {
				return textsanitize.MarkdownSource(summary)
			}
			return ""
		}
	}
	return ""
}
