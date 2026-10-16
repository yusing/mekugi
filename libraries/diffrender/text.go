package diffrender

// Source: internal/livediff/model.go@[b95dde39]
import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/goutils/synk"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Only text and SGR colors may reach the viewport. In particular, captured
// source must not emit cursor controls, OSC clipboard writes, or terminal titles.
func Safe(text string, colors bool) string {
	var local [512]byte
	out := local[:0]
	// Tabs are the only expansion. Bound the borrowed storage up front so
	// appending never abandons a pooled allocation or returns an alias to it.
	if size := len(text) + 3*strings.Count(text, "\t"); size > len(local) {
		pool := synk.GetSizedBytesPool()
		buf := pool.GetSized(size)
		defer func() { clear(buf); pool.Put(buf) }()
		out = buf[:0]
	}
	var state byte
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		state, text = next, text[n:]
		if width > 0 || seq == "\n" {
			out = append(out, seq...)
		} else if seq == "\t" {
			out = append(out, "    "...)
		} else if colors && strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") &&
			strings.Trim(seq[2:len(seq)-1], "0123456789;:") == "" {
			out = append(out, seq...)
		} else if r, _ := utf8.DecodeRuneInString(seq); unicode.IsMark(r) || r == '\u200c' || r == '\u200d' {
			// Zero-width marks and joiners are source text, not terminal controls.
			out = append(out, seq...)
		}
	}
	return string(out)
}

func Gutter(highlighted bool, theme Theme) string {
	if highlighted {
		return theme.Accent() + "▎\x1b[0m "
	}
	return "  "
}

// Give each file a clear section boundary without painting over source colors.
func Header(text string, width int, counts diffmodel.Counts, theme Theme) string {
	width = max(0, width)
	stats := fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), counts.Added, theme.Foreground(chroma.GenericDeleted), counts.Removed)
	if counts.Added < 0 || counts.Removed < 0 {
		stats = " counts unavailable"
	}
	text = ansi.Truncate(Safe(text, false), max(0, width-ansi.StringWidth(stats)), "")
	header := ansi.Truncate("\x1b[1m"+text+"\x1b[22m"+stats, width, "")
	if remaining := width - ansi.StringWidth(header); remaining > 0 {
		header += Subtle + " " + strings.Repeat("─", remaining-1) + SubtleReset
	}
	return header
}

const MaxSourceBytes = 64 << 20

type Output struct{ strings.Builder }

// WriteString bounds rendered output without per-token byte-slice allocation.
func (b *Output) WriteString(s string) (int, error) {
	if b.Len()+len(s) > MaxSourceBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.WriteString(s)
}
