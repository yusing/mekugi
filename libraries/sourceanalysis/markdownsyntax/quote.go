package markdownsyntax

import "strings"

// Source: apps/mekugi/internal/router/commentary.go:35:51@[9822b997].
// QuoteCode keeps backticks in names or previews from ending the code span.
func QuoteCode(value string) string {
	longest, run := 0, 0
	for _, r := range value {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return fence + " " + value + " " + fence
	}
	return fence + value + fence
}
