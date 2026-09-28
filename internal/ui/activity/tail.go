package activity

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// Bounded tails show the latest rows of something still growing and count
// what they leave out. Live thinking tails its painted rows; command output
// tails its lines as they stream, in memory bounded by the tail.
const (
	OutputTailLines = 5
	OutputTailBytes = 512 // Per line, before sanitizing.
	// A burst rolls through at most this many of its latest lines.
	OutputPendingLines = 64
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
	var out strings.Builder
	out.WriteString("\x1b[4m")
	var state byte
	for len(text) > 0 {
		seq, _, n, next := ansi.DecodeSequence(text, state, nil)
		out.WriteString(seq)
		// Nested colors and syntax highlighting can reset all attributes or
		// explicitly end an underline. Keep the hover decoration throughout.
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			out.WriteString("\x1b[4m")
		}
		text, state = text[n:], next
	}
	out.WriteString("\x1b[24m")
	return out.String()
}

// Match the statBar producer's styled span, not bar-like filename text.
var editStatBarPattern = regexp.MustCompile(regexp.QuoteMeta(Green) + "━*" + regexp.QuoteMeta(Red) + "━*" + regexp.QuoteMeta("\x1b[39m"+Dim) + "━*" + regexp.QuoteMeta(Undim))
var editGutterPattern = regexp.MustCompile(`^ *(?:[│└├]─? )? *`)

// UnderlineEdit marks text, not the tree gutter, alignment gaps, or stat bar.
func UnderlineEdit(text string) string {
	var out strings.Builder
	var state byte
	plain := ansi.Strip(text)
	gutter := ansi.StringWidth(editGutterPattern.FindString(plain))
	bar := -1
	if span := editStatBarPattern.FindStringIndex(text); span != nil {
		bar = ansi.StringWidth(text[:span[0]])
	}
	column := 0
	underlined := false
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, nil)
		if width > 0 {
			r, _ := utf8.DecodeRuneInString(seq)
			mark := !unicode.IsSpace(r) && column >= gutter && !(bar >= 0 && column >= bar && column < bar+statBarCells)
			if mark != underlined {
				if mark {
					out.WriteString("\x1b[4m")
				} else {
					out.WriteString("\x1b[24m")
				}
				underlined = mark
			}
			column += width
		}
		out.WriteString(seq)
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			out.WriteString("\x1b[24m")
			underlined = false
		}
		text, state = text[n:], next
	}
	out.WriteString("\x1b[24m")
	return out.String()
}

// OutputTail keeps the display tail of command output as it arrives, in
// memory bounded by the tail regardless of output size. A carriage return
// starts its line over, as a terminal redraws a progress line. Complete lines
// wait until revealed, so a burst can scroll through rather than jump.
type OutputTail struct {
	lines   []outputLine // Last revealed non-blank complete lines, sanitized.
	pending []outputLine // Complete lines awaiting a reveal, oldest first.
	line    []byte       // Current line's head, one byte past the display bound.
	count   int          // Lines before the current line.
	cr      bool         // A carriage return awaits the next byte.
}

type outputLine struct {
	index int
	text  string
}

// TailOutput is the display tail of complete output.
func TailOutput(output string) ([]string, int) {
	var tail OutputTail
	tail.Write(output)
	tail.Reveal(len(tail.pending))
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
				t.pending = append(t.pending, outputLine{t.count, text})
				if len(t.pending) > OutputPendingLines {
					t.pending = slices.Delete(t.pending, 0, 1)
				}
			}
			t.count++
			t.line = t.line[:0]
		case len(t.line) <= OutputTailBytes:
			t.line = append(t.line, c)
		}
	}
}

// Pending counts complete lines awaiting a reveal.
func (t *OutputTail) Pending() int {
	return len(t.pending)
}

// Reveal shows up to n pending lines, oldest first, and reports whether it
// showed any.
func (t *OutputTail) Reveal(n int) bool {
	n = min(n, len(t.pending))
	for _, line := range t.pending[:n] {
		t.lines = append(t.lines, line)
		if len(t.lines) > OutputTailLines {
			t.lines = slices.Delete(t.lines, 0, 1)
		}
	}
	t.pending = slices.Delete(t.pending, 0, n)
	return n > 0
}

// Roll reveals one frame's share of pending lines: a quarter of the backlog,
// at least one. A burst scrolls through in a few hundred milliseconds at the
// frame rate, while steady output stays within a few lines of live.
func (t *OutputTail) Roll() bool {
	return t.Reveal((len(t.pending) + 3) / 4)
}

// Lines is the last revealed non-blank lines, then an unfinished one once
// nothing is pending, and the count of lines before the first of them.
func (t *OutputTail) Lines() ([]string, int) {
	lines := t.lines
	if text := outputText(t.line); text != "" && len(t.pending) == 0 {
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
