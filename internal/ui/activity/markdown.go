package activity

import (
	"strings"
)

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

// closesFence recognizes a closing line for an already-open fence.
func closesFence(line, fence string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	rest, ok := strings.CutPrefix(trimmed, fence)
	return ok && strings.Trim(strings.TrimLeft(rest, fence[:1]), " \t") == ""
}
