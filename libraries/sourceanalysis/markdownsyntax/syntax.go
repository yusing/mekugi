// Package markdownsyntax recognizes the source delimiters shared by activity
// interpretation and terminal Markdown painting.
package markdownsyntax

import (
	"strings"
)

// Source: internal/ui/activity/markdown.go:7:26@[b5ba67e3] fence boundaries.
func FenceDelimiter(line string) (string, bool) {
	ticks := 0
	for ticks < len(line) && line[ticks] == '`' {
		ticks++
	}
	if ticks < 3 || strings.ContainsRune(line[ticks:], '`') {
		return "", false
	}
	return line[:ticks], true
}

// ClosesFence recognizes a closing line for an already-open fence.
func ClosesFence(line, fence string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	rest, ok := strings.CutPrefix(trimmed, fence)
	return ok && strings.Trim(strings.TrimLeft(rest, fence[:1]), " \t") == ""
}

// Source: internal/ui/activity/blocks.go:380:397@[b5ba67e3] inline code spans.
// CodeSpan reads one Markdown code span starting at i.
func CodeSpan(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '`' {
		return "", i, false
	}
	run := 1
	for i+run < len(s) && s[i+run] == '`' {
		run++
	}
	end := strings.Index(s[i+run:], strings.Repeat("`", run))
	if end < 0 {
		return "", i, false
	}
	code := s[i+run : i+run+end]
	if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' {
		code = code[1 : len(code)-1]
	}
	return code, i + run + end + run, true
}

// Source: modules/activity/presentation/source.go:104:116@6f68e18a.
// SourceFence preserves source bytes inside a collision-free Markdown fence.
func SourceFence(language, input string) string {
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	trailer := "\n"
	if strings.HasSuffix(input, "\n") {
		trailer = ""
	}
	return fence + language + "\n" + input + trailer + fence
}
