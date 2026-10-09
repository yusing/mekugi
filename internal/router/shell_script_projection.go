package router

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

const shellInterpreterNamePattern = `python(?:[0-9]+(?:\.[0-9]+)*)?|pypy[0-9]*|node(?:js)?|bun|` +
	`bash|dash|fish|ksh|mksh|sh|yash|perl|ruby|php|lua(?:jit)?|` +
	`r(?:script)?|psql|mysql|sqlite3|pwsh|powershell`

var (
	shellInterpreterPattern = regexp.MustCompile(`(?i)^(?:` + shellInterpreterNamePattern + `)$`)
	shellCommandFlagPattern = regexp.MustCompile(`^-[euilx]*c$`)
)

func shellInterpreterFlag(name, flag string) (source, harmless bool) {
	switch {
	case strings.HasPrefix(name, "python"), strings.HasPrefix(name, "pypy"):
		return flag == "-c", flag == "-I" || flag == "-u" || flag == "-B" || flag == "-E" || flag == "-s" || flag == "-S"
	case name == "node" || name == "nodejs" || name == "bun":
		return flag == "-e" || flag == "--eval", flag == "--input-type=module" || flag == "--input-type=commonjs" || flag == "--trace-warnings"
	case name == "bash" || name == "sh" || name == "dash" || name == "ksh" || name == "mksh" || name == "yash":
		return shellCommandFlagPattern.MatchString(flag), flag == "-e" || flag == "-u" || flag == "-x" || flag == "-l" || flag == "-i"
	case name == "fish":
		return flag == "-c" || flag == "--command", false
	case name == "php":
		return flag == "-r", false
	case name == "psql":
		return flag == "-c" || flag == "--command", false
	case name == "mysql":
		return flag == "-e" || flag == "--execute", false
	case name == "perl" || name == "ruby" || name == "r" || name == "rscript" || strings.HasPrefix(name, "lua"):
		return flag == "-e", false
	default:
		return false, false
	}
}

// shellScriptProjection is display-only. It exposes literal interpreter source
// without changing, evaluating, or claiming completion of the observed command.
type shellScriptProjection struct {
	Source   string
	Language string
	// Command is the invocation around Source, as `python3 -c … arg`.
	Command string
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
		// args is call.Args[1:]: the program is call.Args[index+2].
		words := slices.Clone(call.Args[:index+3])
		words[index+2] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "…"}}}
		words = append(words, args[index+2:]...)
		return shellScriptProjection{Source: program, Language: toolActivityLanguage(name), Command: shellWords(words)}, true
	}

	if len(statement.Redirs) != 1 {
		return shellScriptProjection{}, false
	}
	program, ok := liveDiffShellHeredoc(statement.Redirs[0], partialLine)
	if !ok {
		return shellScriptProjection{}, false
	}
	return shellScriptProjection{Source: program, Language: toolActivityLanguage(name), Command: shellWords(call.Args)}, true
}

// shellInterpreterReadsStdin reports a lone interpreter told by `-` to read its
// program from stdin with nothing redirected there, as `python3 -`.
func shellInterpreterReadsStdin(input string) bool {
	statements, _, _, ok := liveDiffShellStatements(input, "")
	if !ok || len(statements) != 1 {
		return false
	}
	statement := statements[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown ||
		len(statement.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 2 {
		return false
	}
	interpreter, literal := shellCatLiteral(call.Args[0])
	name := shellsyntax.InterpreterIdentity(interpreter)
	if !literal || !shellInterpreterPattern.MatchString(name) {
		return false
	}
	stdin := false
	for _, word := range call.Args[1:] {
		value, static := shellCatLiteral(word)
		if _, harmless := shellInterpreterFlag(name, value); !static || value != "-" && !harmless {
			return false
		}
		stdin = stdin || value == "-"
	}
	return stdin
}

// shellWords prints static words as the command line shows them.
func shellWords(words []*syntax.Word) string {
	shown := make([]string, 0, len(words))
	for _, word := range words {
		var source strings.Builder
		if syntax.NewPrinter().Print(&source, word) != nil || strings.ContainsAny(source.String(), "\r\n") {
			return ""
		}
		shown = append(shown, source.String())
	}
	return strings.Join(shown, " ")
}
