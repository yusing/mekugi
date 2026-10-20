package shellsyntax

// Source: apps/mekugi/internal/router/subagent_tool_display_shell.go:284:308,511:537,596:613@0a356401.

import (
	"path/filepath"

	"mvdan.cc/sh/v3/syntax"
)

// UnwrapEnv removes command-resolution-preserving literal env arguments from a
// copied statement. Unsupported options, dynamic values and PATH changes stay intact.
func UnwrapEnv(statement *syntax.Stmt) *syntax.Stmt {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) < 2 {
		return statement
	}
	name, literal := Literal(call.Args[0])
	if !literal || CommandName(name) != "env" {
		return statement
	}
	args, reason := EnvArgs(call.Args[1:])
	if reason != "" || len(args) == 0 {
		return statement
	}
	innerCall, innerStatement := *call, *statement
	innerCall.Args = args
	innerStatement.Cmd = &innerCall
	return &innerStatement
}

// CommandName normalizes absolute executable paths without treating relative
// paths as known executables.
func CommandName(name string) string {
	if filepath.IsAbs(name) {
		return filepath.Base(name)
	}
	return name
}

// PatternCall returns unevaluated arguments from a simple foreground command.
// An empty source requires literal arguments; otherwise simple shell patterns and
// parameters retain their original spelling. Transparent env and timeout wrappers
// are removed from the copy, never from the executed source.
func PatternCall(script string, statement *syntax.Stmt) ([]string, bool) {
	statement = UnwrapTimeout(statement)
	if statement == nil || len(statement.Redirs) != 0 || statement.Background || statement.Negated ||
		statement.Coprocess || statement.Disown {
		return nil, false
	}
	if inner := UnwrapEnv(statement); inner != statement {
		return PatternCall(script, inner)
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 {
		return nil, false
	}
	argv := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		value, literal := Literal(arg)
		if !literal {
			if script == "" || !PatternWord(arg) {
				return nil, false
			}
			value = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
		}
		argv = append(argv, value)
	}
	argv[0] = CommandName(argv[0])
	return argv, true
}

// PatternWord recognizes shell patterns and simple parameter references without
// expanding them. Parameter operators and executable substitutions stay opaque.
func PatternWord(word *syntax.Word) bool {
	valid := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, nil:
		case *syntax.ParamExp:
			valid = valid && node.Param != nil && node.Flags == nil &&
				!node.Excl && !node.Length && !node.Width && !node.IsSet &&
				node.Split == syntax.OptUnset && node.GlobSubst == syntax.OptUnset && node.RcExpand == syntax.OptUnset &&
				node.NestedParam == nil && node.Index == nil && len(node.Modifiers) == 0 &&
				node.Slice == nil && node.Repl == nil && node.Names == 0 && node.Exp == nil
		default:
			valid = false
		}
		return valid
	})
	return valid
}
