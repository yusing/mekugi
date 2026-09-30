package activity

import (
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"
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
		return p.Highlight("diff", content)
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

// Color from retained context before selecting the animated tail. Blank rows
// are absent from the tail, and its last revealed row may precede live output.
func (p *Painter) tailColors(block Block) []string {
	if block.Output == nil || len(block.Tail) == 0 {
		return p.outputColors(block, block.Tail)
	}
	view := block.Output.View()
	start := block.TailOmitted - view.Dropped
	if view.Released || start < 0 || start >= len(view.Lines) {
		return p.outputColors(block, block.Tail)
	}
	indexes := make([]int, 0, len(block.Tail))
	for i := start; i < len(view.Lines) && len(indexes) < len(block.Tail); i++ {
		text := outputText([]byte(view.Lines[i]))
		if text == "" {
			continue
		}
		if text != block.Tail[len(indexes)] {
			return p.outputColors(block, block.Tail)
		}
		indexes = append(indexes, i)
	}
	if len(indexes) != len(block.Tail) {
		return p.outputColors(block, block.Tail)
	}
	colored := p.outputColors(block, view.Lines[:indexes[len(indexes)-1]+1])
	tail := make([]string, len(indexes))
	for i, index := range indexes {
		tail[i] = colored[index]
		if view.Lines[index] != block.Tail[i] {
			tail[i] = ansi.Truncate(tail[i], ansi.StringWidth(block.Tail[i]), "…")
		}
	}
	return tail
}
