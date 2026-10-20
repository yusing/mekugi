package shellsyntax

import (
	"path/filepath"
	"regexp"

	"mvdan.cc/sh/v3/syntax"
)

// Source: apps/mekugi/internal/router/subagent_tool_display_shell.go:279:312@[9822b997].
// A plain timeout changes the execution bound, not the displayed operation.
var timeoutDuration = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)[smhd]?$`)

// Unwrap only a display copy. Keep argument positions, redirections, and all
// host-owned source bytes; unsupported options and dynamic durations stay Run.
func UnwrapTimeout(statement *syntax.Stmt) *syntax.Stmt {
	if statement == nil {
		return statement
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 {
		return statement
	}
	args := call.Args
	for len(args) >= 3 {
		name, literal := Literal(args[0])
		if !literal || filepath.Base(name) != "timeout" {
			break
		}
		duration, literal := Literal(args[1])
		if !literal || !timeoutDuration.MatchString(duration) {
			return statement
		}
		args = args[2:]
	}
	if len(args) == len(call.Args) {
		return statement
	}
	innerCall, innerStatement := *call, *statement
	innerCall.Args = args
	innerStatement.Cmd = &innerCall
	return &innerStatement
}
