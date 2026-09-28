package activity

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// Bounded tails show the latest rows of something still growing and count
// what they leave out. Live thinking tails its painted rows; command output
// tails its lines as they stream, in memory bounded by the tail.
const (
	OutputTailLines = 5
	OutputTailBytes = 512 // Per line, before sanitizing.
)

// BlankRow reports a painted row without text: a paragraph gap or an empty
// quote row.
func BlankRow(row string) bool {
	return strings.Trim(ansi.Strip(row), " │▎") == ""
}

// TailRows keeps the last limit rows with text and counts the text rows
// before them. Paragraph gaps take no place, so the tail always shows text.
func TailRows(rows []string, limit int) ([]string, int) {
	var tail []string
	hidden := 0
	for _, row := range slices.Backward(rows) {
		switch {
		case BlankRow(row):
		case len(tail) < limit:
			tail = append(tail, row)
		default:
			hidden++
		}
	}
	slices.Reverse(tail)
	return tail, hidden
}

// MoreLines labels n rows left out of view.
func MoreLines(n int) string {
	if n == 1 {
		return "+1 line"
	}
	return fmt.Sprintf("+%d lines", n)
}

// Underline marks a row's toggle under the pointer.
func Underline(text string) string {
	return "\x1b[4m" + text + "\x1b[24m"
}

// OutputTail keeps the display tail of command output as it arrives, in
// memory bounded by the tail regardless of output size. A carriage return
// starts its line over, as a terminal redraws a progress line.
type OutputTail struct {
	lines []outputLine // Last non-blank complete lines, sanitized.
	line  []byte       // Current line's head, one byte past the display bound.
	count int          // Lines before the current line.
	cr    bool         // A carriage return awaits the next byte.
}

type outputLine struct {
	index int
	text  string
}

// TailOutput is the display tail of complete output.
func TailOutput(output string) ([]string, int) {
	var tail OutputTail
	tail.Write(output)
	return tail.Lines()
}

func (t *OutputTail) Write(output string) {
	for i := range len(output) {
		c := output[i]
		if t.cr && c != '\n' {
			t.line = t.line[:0]
		}
		t.cr = false
		switch {
		case c == '\r':
			t.cr = true
		case c == '\n':
			if text := outputText(t.line); text != "" {
				t.lines = append(t.lines, outputLine{t.count, text})
				if len(t.lines) > OutputTailLines {
					t.lines = slices.Delete(t.lines, 0, 1)
				}
			}
			t.count++
			t.line = t.line[:0]
		case len(t.line) <= OutputTailBytes:
			t.line = append(t.line, c)
		}
	}
}

// Lines is the last non-blank lines, including an unfinished one, and the
// count of lines before the first of them.
func (t *OutputTail) Lines() ([]string, int) {
	lines := t.lines
	if text := outputText(t.line); text != "" {
		lines = append(slices.Clip(lines), outputLine{t.count, text})
	}
	lines = lines[max(0, len(lines)-OutputTailLines):]
	if len(lines) == 0 {
		return nil, 0
	}
	tail := make([]string, len(lines))
	for i, line := range lines {
		tail[i] = line.text
	}
	return tail, lines[0].index
}

// outputText sanitizes one bounded line; blank lines carry nothing.
func outputText(raw []byte) string {
	line := string(raw)
	if len(line) > OutputTailBytes {
		line = strings.ToValidUTF8(line[:OutputTailBytes], "") + "…"
	}
	if line = strings.TrimRight(livediff.Safe(line, false), " "); strings.TrimSpace(line) != "" {
		return line
	}
	return ""
}
