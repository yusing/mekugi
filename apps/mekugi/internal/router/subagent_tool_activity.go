package router

import (
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

func toolActivityCode(input string) string {
	if !strings.ContainsAny(input, "\r\n") {
		return markdownsyntax.QuoteCode(input)
	}
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	return fence + "\n" + input + "\n" + fence
}
