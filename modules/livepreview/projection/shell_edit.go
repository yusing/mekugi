package projection

import (
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

func ShellNeutral(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated || len(stmt.Redirs) != 0 {
		return false
	}
	if len(call.Args) == 0 {
		// Literal shell variables change no file; a later use of one is not
		// a literal operand and is not predicted.
		for _, assign := range call.Assigns {
			if assign.Append || assign.Array != nil || assign.Index != nil {
				return false
			}
			if _, literal := shellsyntax.Literal(assign.Value); assign.Value != nil && !literal {
				return false
			}
		}
		return len(call.Assigns) != 0
	}
	if len(call.Assigns) != 0 {
		return false
	}
	name, literal := shellsyntax.Literal(call.Args[0])
	if !literal {
		return false
	}
	switch name {
	case "mkdir", "mread", "mcat", "msymbol", "inspect_file", "mchanges", "set", "echo", "true":
	default:
		return false
	}
	for index, argument := range call.Args[1:] {
		value, literal := shellsyntax.Literal(argument)
		// mchanges revert and apply rewrite files that later previews read.
		if !literal || index == 0 && name == "mchanges" && (value == "revert" || value == "apply") {
			return false
		}
	}
	return true
}
