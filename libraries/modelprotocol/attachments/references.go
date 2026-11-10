package attachments

import (
	"iter"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDepth includes the explicitly selected attachment.
const MaxDepth = 3

// References recognizes @path tokens in selected text, including JSON string
// values and quoted prose. Email addresses and embedded identifier suffixes
// remain literal, as do bare @ and diff markers beginning with @@. Quoting
// after @ permits spaces or a leading @ in a path.
func References(text string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for i := 0; i < len(text); i++ {
			if text[i] != '@' {
				continue
			}
			if i > 0 {
				previous, _ := utf8.DecodeLastRuneInString(text[:i])
				if !unicode.IsSpace(previous) && !strings.ContainsRune("\"'`([{:", previous) {
					continue
				}
			}
			start := i + 1
			if start == len(text) || text[start] == '@' {
				continue
			}
			end := start
			if quote := text[start]; quote == '\'' || quote == '"' {
				start++
				at := strings.IndexByte(text[start:], quote)
				if at < 0 {
					continue
				}
				end = start + at
				i = end
			} else {
				for end < len(text) {
					r, size := utf8.DecodeRuneInString(text[end:])
					if unicode.IsSpace(r) || strings.ContainsRune("\"'`()[]{}<>,;", r) {
						break
					}
					end += size
				}
				i = end - 1
			}
			if end > start && !yield(text[start:end]) {
				return
			}
		}
	}
}
