package commandargs

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Source: libraries/commandanalysis/shellsyntax/source.go:9:12@7197229d.
// Quote preserves a literal shell argument, including empty strings.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// QuoteArgument renders a compact Bash argument, preserving the literal fallback.
// Source: libraries/commandanalysis/shellsyntax/words.go:210:216@7197229d.
func QuoteArgument(value string) string {
	quoted, err := syntax.Quote(value, syntax.LangBash)
	if err == nil {
		return quoted
	}
	return Quote(value)
}
