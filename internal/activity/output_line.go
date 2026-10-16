package activity

// Source: internal/ui/activity/output_line.go@[11705e68]
import (
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"
)

// LineBuffer assembles bounded streamed rows. Consumers own completed
// line retention and reset Line in endLine. Line is borrowed mutable scratch
// storage, bounded to limit+1 bytes so consumers can detect truncation.
type LineBuffer struct {
	Line []byte
	cr   bool
}

func (b *LineBuffer) Write(output string, limit int, endLine func()) {
	for i := range len(output) {
		c := output[i]
		if b.cr && c != '\n' {
			b.Line = b.Line[:0]
		}
		b.cr = false
		switch {
		case c == '\r':
			b.cr = true
		case c == '\n':
			endLine()
		case len(b.Line) <= limit:
			b.Line = append(b.Line, c)
		}
	}
}

// LineText normalizes a bounded line for both retained content and display tails.
func LineText(raw []byte, limit int) string {
	line := string(raw)
	if len(line) > limit {
		line = strings.ToValidUTF8(line[:limit], "") + "…"
	}
	return strings.TrimRight(textsanitize.Safe(line, false), " ")
}
