package shellsyntax

import (
	"github.com/yusing/mekugi/libraries/commandargs"
	"mvdan.cc/sh/v3/syntax"
)

// UnwrapMRun removes valid literal mrun options from a display copy. Child
// arguments and redirections remain intact; this does not establish execution.
func UnwrapMRun(statement *syntax.Stmt) *syntax.Stmt {
	if statement == nil {
		return statement
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) < 2 {
		return statement
	}
	name, literal := Literal(call.Args[0])
	if !literal || CommandName(name) != "mrun" {
		return statement
	}
	args := make([]string, len(call.Args)-1)
	for i, word := range call.Args[1:] {
		// Dynamic wrapper arguments cannot be valid limits or executables.
		// Words after the executable belong to the child, not this grammar.
		args[i], _ = Literal(word)
	}
	_, child, err := commandargs.ParseRunOptions(args)
	if err != nil {
		return statement
	}
	innerCall, innerStatement := *call, *statement
	innerCall.Args = call.Args[len(call.Args)-len(child):]
	innerStatement.Cmd = &innerCall
	return &innerStatement
}
