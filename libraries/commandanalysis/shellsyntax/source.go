package shellsyntax

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Quote preserves a literal shell argument, including empty strings.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// StatementSource prints a statement that owns a noncontiguous heredoc body.
// Other statements keep the caller's source slice and separator policy.
func StatementSource(stmt *syntax.Stmt, fallback string) string {
	heredoc := false
	syntax.Walk(stmt, func(node syntax.Node) bool {
		if redirect, ok := node.(*syntax.Redirect); ok && redirect.Hdoc != nil {
			heredoc = true
		}
		return !heredoc
	})
	if heredoc {
		var printed strings.Builder
		if syntax.NewPrinter().Print(&printed, stmt) == nil {
			return strings.TrimRight(printed.String(), "\n")
		}
	}
	return fallback
}
