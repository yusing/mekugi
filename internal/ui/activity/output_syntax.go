package activity

import (
	"context"
	"strings"
)

// Output syntax follows the content, never the command that produced it.
// Both inline tails and dialogs use the same bounded renderer and cache.
func (p *Painter) outputColors(block Block, rows []string) []string {
	if len(rows) == 0 {
		return rows
	}
	content := strings.Join(rows, "\n")
	if len(content) > dialogHighlightBytes {
		return rows
	}
	path := ""
	switch {
	case block.ReadOutput() && len(block.Reads) == 1:
		path = block.Reads[0].Path
	case block.Verb == "Diff" && block.VCS():
		path = "output.diff"
	case strings.Contains(content, "\n+++ ") && strings.Contains(content, "\n@@ "):
		return p.Highlight("diff", content)
	case block.Verb == "Search":
		colored := make([]string, len(rows))
		for i, line := range rows {
			colored[i] = p.dialogSearchLine(block, line)
		}
		return colored
	}
	// Supply a terminator so a retained trailing blank row keeps its position.
	colored, err := p.syntax.ColorSource(context.Background(), p.Theme, path, content+"\n")
	if err != nil || len(colored) != len(rows) {
		return rows
	}
	return colored
}
