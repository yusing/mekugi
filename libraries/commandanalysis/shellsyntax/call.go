package shellsyntax

// Source: apps/mekugi/internal/router/subagent_tool_display_shell.go:284:308,511:537,596:613@0a356401.

import (
	"path/filepath"
	"strings"

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

// ShellScript recognizes a literal shell -c script without positional arguments
// or redirects. Recognition does not execute the script or resolve executables.
func ShellScript(statement *syntax.Stmt) (string, bool) {
	argv, ok := PatternCall("", statement)
	if !ok || len(argv) != 3 || (argv[0] != "bash" && argv[0] != "sh" && argv[0] != "dash") || (argv[1] != "-c" && argv[1] != "-lc") {
		return "", false
	}
	return argv[2], true
}

// ShellOptions recognizes current-shell set options that neither change command
// resolution nor select a repository or directory. External wrappers stay opaque.
func ShellOptions(statement *syntax.Stmt) bool {
	_, ok := ShellErrexit(statement)
	return ok
}

// ShellErrexit reports whether validated current-shell error options explicitly
// leave errexit enabled. Unspecified errexit is conservatively false.
func ShellErrexit(statement *syntax.Stmt) (bool, bool) {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false, false
	}
	if name, literal := Literal(call.Args[0]); !literal || name != "set" {
		return false, false
	}
	argv, ok := PatternCall("", statement)
	if !ok || len(argv) < 2 || argv[0] != "set" {
		return false, false
	}
	errexit := false
	for i := 1; i < len(argv); i++ {
		option := argv[i]
		if len(option) >= 2 && (option[0] == '-' || option[0] == '+') && strings.HasSuffix(option, "o") && strings.Trim(option[1:len(option)-1], "eu") == "" {
			i++
			if i >= len(argv) || (argv[i] != "errexit" && argv[i] != "nounset" && argv[i] != "pipefail") {
				return false, false
			}
			if strings.Contains(option[1:], "e") || argv[i] == "errexit" {
				errexit = option[0] == '-'
			}
		} else if len(option) < 2 || (option[0] != '-' && option[0] != '+') || strings.Trim(option[1:], "eu") != "" {
			return false, false
		} else if strings.Contains(option[1:], "e") {
			errexit = option[0] == '-'
		}
	}
	return errexit, true
}
