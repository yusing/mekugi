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
