package router

import (
	"strings"
)

func toolActivityCode(input string) string {
	if !strings.ContainsAny(input, "\r\n") {
		return commentaryCode(input)
	}
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	return fence + "\n" + input + "\n" + fence
}
