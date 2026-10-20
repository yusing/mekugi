package textsanitize

// Source: libraries/diffrender/text.go:17:51@[8bb15667]
import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/goutils/synk"
)

// Safe preserves readable text and optional SGR colors, removing other controls
// and expanding tabs. Input cannot emit cursor moves, clipboard writes or titles.
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

// Source: libraries/terminalui/copy.go:505:513@35fd8de4.
// MarkdownSource removes terminal controls without normalizing code tabs. The
// shared renderer expands them for display and retains their source identity.
func MarkdownSource(text string) string {
	parts := strings.Split(text, "\t")
	for i := range parts {
		parts[i] = Safe(parts[i], false)
	}
	return strings.Join(parts, "\t")
}
