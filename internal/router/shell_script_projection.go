package router

import (
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// shellScriptProjection is display-only. It exposes literal interpreter source
// without changing, evaluating, or claiming completion of the observed command.
type shellScriptProjection struct {
	Source   string
	Language string
}

func shellInterpreterScriptProjection(input string) (shellScriptProjection, bool) {
	statements, _, partialLine, ok := liveDiffShellStatements(input, "")
	if !ok || len(statements) != 1 {
		return shellScriptProjection{}, false
	}
	statement := statements[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown ||
		len(call.Assigns) != 0 || len(call.Args) == 0 {
		return shellScriptProjection{}, false
	}
	interpreter, literal := shellCatLiteral(call.Args[0])
	name := shellsyntax.InterpreterIdentity(interpreter)
	if !literal || !shellInterpreterPattern.MatchString(name) {
		return shellScriptProjection{}, false
	}

	args := call.Args[1:]
	for index, word := range args {
		value, static := shellCatLiteral(word)
		if !static {
			return shellScriptProjection{}, false
		}
		if value == "-" {
			continue
		}
		if !strings.HasPrefix(value, "-") || value == "--" || strings.ContainsAny(value, " \t\r\n") {
			return shellScriptProjection{}, false
		}
		source, harmless := shellInterpreterFlag(name, value)
		if !source {
			if !harmless {
				return shellScriptProjection{}, false
			}
			continue
		}
		if index+1 >= len(args) || len(statement.Redirs) != 0 {
			return shellScriptProjection{}, false
		}
		program, static := shellCatLiteral(args[index+1])
		if !static {
			return shellScriptProjection{}, false
		}
		for _, trailing := range args[index+2:] {
			value, static := shellCatLiteral(trailing)
			if !static || strings.HasPrefix(value, "-") {
				return shellScriptProjection{}, false
			}
		}
		return shellScriptProjection{Source: program, Language: toolActivityLanguage(name)}, true
	}

	if len(statement.Redirs) != 1 {
		return shellScriptProjection{}, false
	}
	program, ok := liveDiffShellHeredoc(statement.Redirs[0], partialLine)
	if !ok {
		return shellScriptProjection{}, false
	}
	return shellScriptProjection{Source: program, Language: toolActivityLanguage(name)}, true
}
