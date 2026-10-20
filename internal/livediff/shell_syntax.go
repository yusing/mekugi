package livediff

import (
	"strings"

	Syntax "mvdan.cc/sh/v3/syntax"
)

// Chroma's Bash lexer treats external commands as plain text. Use shell grammar,
// not a command-name allowlist, to distinguish command words from arguments,
// assignment values, comments, and heredoc bodies. This is decoration only.
// The renderer has already bounded source size; incomplete input may still
// provide useful parsed statements before the first unrecoverable syntax error.
func shellCommands(source string) map[int]string {
	program, _ := Syntax.NewParser(Syntax.RecoverErrors(8)).Parse(strings.NewReader(source), "")
	if program == nil {
		return nil
	}
	var commands map[int]string
	Syntax.Walk(program, func(node Syntax.Node) bool {
		call, ok := node.(*Syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		word := call.Args[0]
		if literal := word.Lit(); literal != "" && !word.Pos().IsRecovered() {
			if commands == nil {
				commands = make(map[int]string)
			}
			commands[int(word.Pos().Offset())] = literal
		}
		return true
	})
	return commands
}
