package execscope

import (
	"path/filepath"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// StatementIntent derives only explicit rm/mv/sed operands for display. It
// neither resolves filesystem metadata nor claims observed execution effects.
func StatementIntent(statement *syntax.Stmt) Result {
	plan := Result{}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return plan
	}
	command, literal := shellsyntax.Literal(call.Args[0])
	if !literal {
		return plan
	}
	for _, arg := range call.Args[1:] {
		if _, glob, literal := shellsyntax.Operand(arg); !literal || glob {
			return plan
		}
	}
	walker := execShellWalker{cwd: ".", plan: &plan}
	switch name := filepath.Base(command); name {
	case "rm":
		walker.remove(call.Args[1:])
	case "mv":
		walker.copyLike(name, call.Args[1:], true)
	case "sed":
		walker.sed(call.Args[1:])
	}
	return plan
}
