package feed

// Source: modules/activity/terminalview/tail.go:115:219@7166de95.

import (
	"slices"
	"strings"
)

// Summary bounds are content limits, independent of terminal width or reveal.
const SummaryLines = 5
const SummaryLineBytes = 512

// SummaryTail retains bounded logical source lines without an animation cursor.
type SummaryTail struct {
	buffer LineBuffer
	lines  []summaryLine
	count  int
}
type summaryLine struct {
	index   int
	text    string
	clipped bool
}

func (t *SummaryTail) Write(output string) {
	t.buffer.Write(output, SummaryLineBytes, func() {
		if text := LineText(t.buffer.Line, SummaryLineBytes); strings.TrimSpace(text) != "" {
			t.lines = append(t.lines, summaryLine{t.count, text, len(t.buffer.Line) > SummaryLineBytes})
			if len(t.lines) > SummaryLines {
				t.lines = slices.Delete(t.lines, 0, 1)
			}
		}
		t.count++
		t.buffer.Line = t.buffer.Line[:0]
	})
}

func (t *SummaryTail) Lines() ([]string, int, bool) {
	lines := t.lines
	if text := LineText(t.buffer.Line, SummaryLineBytes); strings.TrimSpace(text) != "" {
		lines = append(slices.Clip(lines), summaryLine{t.count, text, len(t.buffer.Line) > SummaryLineBytes})
	}
	lines = lines[max(0, len(lines)-SummaryLines):]
	if len(lines) == 0 {
		return nil, 0, false
	}
	out := make([]string, len(lines))
	clipped := false
	for i, line := range lines {
		out[i] = line.text
		clipped = clipped || line.clipped
	}
	return out, lines[0].index, clipped
}

func TailOutput(output string) ([]string, int, bool) {
	var tail SummaryTail
	tail.Write(output)
	return tail.Lines()
}
