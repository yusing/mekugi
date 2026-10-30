package presentation

// Source: apps/mekugi/internal/router/shell_script_projection.go:1:159@0a356401.

import (
	"regexp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
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
		return flag == "-e" || flag == "--eval",
			flag == "--input-type=module" || flag == "--input-type=commonjs" || flag == "--trace-warnings" || name == "bun" && flag == "--no-install"
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
	// Via names what runs Source: the invocation without its program, as
	// `python3 -I`, keeping `…` only where arguments follow the program.
	Via string
}

func shellInterpreterScriptProjection(input string) (shellScriptProjection, bool) {
	statements, _, partialLine, ok := shellsyntax.StreamingStatements(input, "")
	if !ok || len(statements) != 1 {
		return shellScriptProjection{}, false
	}
	return shellStatementProjection(statements[0], partialLine)
}

// shellStatementProjection projects a parsed statement, so heredoc bodies
// stay with their own redirects; partial trims an incomplete final body line.
func shellStatementProjection(statement *syntax.Stmt, partial bool) (shellScriptProjection, bool) {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown || len(call.Args) == 0 {
		return shellScriptProjection{}, false
	}
	interpreter, literal := shellsyntax.Literal(call.Args[0])
	name := shellsyntax.InterpreterIdentity(interpreter)
	if !literal || !shellInterpreterPattern.MatchString(name) {
		return shellScriptProjection{}, false
	}
	// Assignments prefix the invocation, so Via shows them as written.
	var assigns strings.Builder
	for _, assign := range call.Assigns {
		var source strings.Builder
		if assign.Append || assign.Naked || !shellPlainWord(assign) ||
			syntax.NewPrinter().Print(&source, assign) != nil || strings.ContainsAny(source.String(), "\r\n") {
			return shellScriptProjection{}, false
		}
		assigns.WriteString(source.String() + " ")
	}

	args := call.Args[1:]
	stdin := -1
	for index, word := range args {
		if stdin >= 0 {
			// After `-`, words are the program's arguments.
			if !shellPlainWord(word) {
				return shellScriptProjection{}, false
			}
			continue
		}
		value, static := shellsyntax.Literal(word)
		if !static {
			return shellScriptProjection{}, false
		}
		if value == "-" {
			// Other interpreters may run a following file argument instead.
			if index+1 < len(args) && !shellInterpreterStdinArguments(name) {
				return shellScriptProjection{}, false
			}
			stdin = index
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
		program, static := shellsyntax.Literal(args[index+1])
		if !static {
			return shellScriptProjection{}, false
		}
		for _, trailing := range args[index+2:] {
			value, static := shellsyntax.Literal(trailing)
			if !shellPlainWord(trailing) || static && strings.HasPrefix(value, "-") {
				return shellScriptProjection{}, false
			}
		}
		// args is call.Args[1:]: the program is call.Args[index+2].
		words := slices.Clone(call.Args[:index+3])
		words[index+2] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "…"}}}
		words = append(words, args[index+2:]...)
		if index+2 == len(args) {
			// Without arguments after the program, its flag adds nothing; a
			// combined shell flag keeps its other options, as `sh -e`.
			words = slices.Clone(call.Args[:index+2])
			if options := strings.TrimSuffix(value, "c"); options != value && options != "-" {
				words[index+1] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: options}}}
			} else {
				words = words[:index+1]
			}
		}
		return shellScriptProjection{Source: program, Language: sourceLanguage(name), Via: shellViaWords(assigns.String(), words)}, true
	}

	if len(statement.Redirs) != 1 {
		return shellScriptProjection{}, false
	}
	program, ok := shellsyntax.Heredoc(statement.Redirs[0], partial)
	if !ok {
		return shellScriptProjection{}, false
	}
	via := call.Args
	if stdin >= 0 && stdin == len(args)-1 && shellInterpreterStdinArguments(name) {
		via = via[:len(via)-1] // A final `-` only says the program is stdin.
	}
	return shellScriptProjection{Source: program, Language: sourceLanguage(name), Via: shellViaWords(assigns.String(), via)}, true
}

// shellInterpreterStdinArguments reports interpreters that read their program
// from stdin given `-` and pass the words after it to the program.
func shellInterpreterStdinArguments(name string) bool {
	return strings.HasPrefix(name, "python") || strings.HasPrefix(name, "pypy") || strings.HasPrefix(name, "lua") ||
		name == "node" || name == "nodejs" || name == "bun" || name == "perl" || name == "ruby"
}

func shellViaWords(assigns string, words []*syntax.Word) string {
	shown := shellWords(words)
	if shown == "" {
		return ""
	}
	return assigns + shown
}

// shellPlainWord reports whether a printed node shows everything it runs.
// Parameters print as written; substitutions would hide their commands.
func shellPlainWord(node syntax.Node) bool {
	plain := true
	syntax.Walk(node, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			plain = false
		}
		return plain
	})
	return plain
}

// shellInterpreterReadsStdin reports a lone interpreter told by `-` to read its
// program from stdin with nothing redirected there, as `python3 -`.
func shellInterpreterReadsStdin(input string) bool {
	statements, _, _, ok := shellsyntax.StreamingStatements(input, "")
	if !ok || len(statements) != 1 {
		return false
	}
	statement := statements[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown ||
		len(statement.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 2 {
		return false
	}
	interpreter, literal := shellsyntax.Literal(call.Args[0])
	name := shellsyntax.InterpreterIdentity(interpreter)
	if !literal || !shellInterpreterPattern.MatchString(name) {
		return false
	}
	stdin := false
	for _, word := range call.Args[1:] {
		value, static := shellsyntax.Literal(word)
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
