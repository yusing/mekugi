package router

import (
	"fmt"

	"mvdan.cc/sh/v3/syntax"
)

// rewriteShellTime uses the real timing utility instead of mvdan's keyword,
// which prints placeholder CPU measurements to stdout.
func rewriteShellTime(program *syntax.File, dispatch string) error {
	var rewriteErr error
	syntax.Walk(program, func(node syntax.Node) bool {
		if rewriteErr != nil {
			return false
		}
		statement, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		timing, ok := statement.Cmd.(*syntax.TimeClause)
		if !ok {
			return true
		}
		var call syntax.CallExpr
		if timing.Stmt != nil {
			original, ok := timing.Stmt.Cmd.(*syntax.CallExpr)
			if !ok || timing.Stmt.Negated || timing.Stmt.Background || timing.Stmt.Coprocess {
				rewriteErr = fmt.Errorf("time: compound shell commands require an explicit interpreter, for example: time bash -c 'command1 | command2'")
				return false
			}
			call = *original
			statement.Redirs = append(statement.Redirs, timing.Stmt.Redirs...)
		}
		words := []*syntax.Word{shellTimeWord(dispatch), shellTimeWord("time")}
		if timing.PosixFormat {
			words = append(words, shellTimeWord("-p"))
		}
		call.Args = append(words, call.Args...)
		statement.Cmd = &call
		return true
	})
	return rewriteErr
}

func shellTimeWord(value string) *syntax.Word {
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: value}}}
}
