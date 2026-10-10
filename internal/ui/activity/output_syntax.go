package activity

import (
	"context"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Untyped command output may select an expensive, unrelated lexer. Keep
// automatic decoration bounded on the paint path; explicit hints and diffs
// retain the larger shared highlighting bound.
const outputAutoHighlightBytes = 8 << 10

// Output syntax follows the content, never the command that produced it.
// Both inline tails and dialogs use the same bounded renderer and cache.
func (p *Painter) outputColors(block Block, rows []string) []string {
	return p.outputColorsAt(block, rows, nil)
}

// outputColorsAt keeps source/diff context, but independent search rows need
// coloring only when selected. A nil selection colors every row for dialogs.
func (p *Painter) outputColorsAt(block Block, rows []string, indexes []int) []string {
	selectRows := func(rows []string) []string {
		if indexes == nil {
			return rows
		}
		selected := make([]string, len(indexes))
		for i, index := range indexes {
			selected[i] = rows[index]
		}
		return selected
	}
	if len(rows) == 0 || p.LayoutOnly {
		return selectRows(rows)
	}
	content := strings.Join(rows, "\n")
	if len(content) > dialogHighlightBytes {
		return selectRows(rows)
	}
	path := ""
	switch {
	case block.Hook != nil:
		return selectRows(p.hookColors(rows))
	case block.ReadOutput() && len(block.Reads) == 1:
		path = block.Reads[0].Path
		if block.SyntaxPath != "" {
			path = block.SyntaxPath
		}
	case block.Verb == "Diff" && block.VCS():
		return selectRows(p.Highlight("diff", content))
	case strings.Contains(content, "\n+++ ") && strings.Contains(content, "\n@@ "):
		return selectRows(p.Highlight("diff", content))
	case block.Verb == "Search":
		rows = selectRows(rows)
		colored := make([]string, len(rows))
		for i, line := range rows {
			colored[i] = p.dialogSearchLine(block, line)
		}
		return colored
	}
	if path == "" && len(content) > outputAutoHighlightBytes {
		return selectRows(rows)
	}
	// Supply a terminator so a retained trailing blank row keeps its position.
	colored, err := p.syntax.ColorSource(context.Background(), p.Theme, path, content+"\n")
	if err != nil || len(colored) != len(rows) {
		return selectRows(rows)
	}
	return selectRows(colored)
}

// Hook output prefixes each host entry with its kind. Detection misreads the
// labeled whole, so each context body is colored alone and keeps its plain
// label; warning, stop, feedback, and error entries are prose and stay plain.
var hookEntryKinds = []string{"warning", "stop", "feedback", "context", "error"}

func hookEntryKind(row string) string {
	for _, kind := range hookEntryKinds {
		if strings.HasPrefix(row, kind+": ") {
			return kind
		}
	}
	return ""
}

func (p *Painter) hookColors(rows []string) []string {
	const label = "context: "
	colored := slices.Clone(rows)
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && hookEntryKind(rows[end]) == "" {
			end++
		}
		if hookEntryKind(rows[start]) == "context" {
			body := strings.TrimPrefix(strings.Join(rows[start:end], "\n"), label)
			if len(body) <= outputAutoHighlightBytes {
				lines, err := p.syntax.ColorSource(context.Background(), p.Theme, "", body+"\n")
				if err == nil && len(lines) == end-start {
					copy(colored[start:end], lines)
					colored[start] = label + colored[start]
				}
			}
		}
		start = end
	}
	return colored
}

// Color from retained context before selecting the animated tail. Blank rows
// are absent from the tail, and its last revealed row may precede live output.
func (p *Painter) tailColors(block Block) []string {
	// The observed tail is already bounded and sanitized. Measuring layout
	// must not revisit and truncate its potentially much larger retained lines.
	if p.LayoutOnly {
		return block.Tail
	}
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
	tail := p.outputColorsAt(block, view.Lines[:indexes[len(indexes)-1]+1], indexes)
	for i, index := range indexes {
		if view.Lines[index] != block.Tail[i] {
			if tail[i] == view.Lines[index] {
				tail[i] = block.Tail[i] // No syntax was added; reuse the exact observed tail.
			} else {
				tail[i] = ansi.Truncate(tail[i], ansi.StringWidth(block.Tail[i]), "…")
			}
		}
	}
	return tail
}
