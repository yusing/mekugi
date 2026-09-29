package router

import (
	"path/filepath"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// Intent is not a receipt. Keep edit source out of Run cards before capture,
// without reading target files or claiming the requested writes succeeded.
func toolActivityEditStatement(statement *syntax.Stmt) (string, bool) {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return "", false
	}
	command, literal := shellCatLiteral(call.Args[0])
	if !literal {
		return "", false
	}
	name := filepath.Base(command)
	if (name == "sed" || name == "rm") && len(statement.Redirs) == 0 {
		// Reuse capture's option/script parser without resolving or reading
		// files. The operation is requested; only its later receipt has counts.
		for _, arg := range call.Args[1:] {
			if _, glob, literal := execWordOperand(arg); !literal || glob {
				return "", false
			}
		}
		plan := execPlan{}
		walker := execShellWalker{cwd: ".", plan: &plan}
		verb := "Edit"
		if name == "rm" {
			walker.remove(call.Args[1:])
			verb = "Delete"
		} else {
			walker.sed(call.Args[1:])
		}
		if plan.Class != execDeclared || len(plan.Scope) == 0 {
			return "", false
		}
		var paths []string
		for _, scope := range plan.Scope {
			for _, operand := range scope.Operands {
				paths = append(paths, verb+" "+toolActivityCode(operand.Path)+" · "+name+" (requested)")
			}
		}
		return strings.Join(paths, "\n\n"), true
	}
	if command == "cat" {
		var paths []string
		for _, redirect := range statement.Redirs {
			if redirect.Op != syntax.RdrOut && redirect.Op != syntax.AppOut && redirect.Op != syntax.RdrClob || redirect.N != nil && redirect.N.Value != "1" {
				continue
			}
			path, literal := shellCatLiteral(redirect.Word)
			if !literal && toolActivityPatternWord(redirect.Word) {
				path = shellWords([]*syntax.Word{redirect.Word})
			}
			if path != "" && !strings.HasPrefix(path, "/dev/") {
				paths = append(paths, "Edit "+toolActivityCode(path)+" · cat (requested)")
			}
		}
		if len(paths) != 0 {
			return strings.Join(paths, "\n\n"), true
		}
	}
	languageName := toolActivityLanguage(command)
	if languageName != "python" && languageName != "javascript" {
		return "", false
	}
	var source strings.Builder
	if syntax.NewPrinter().Print(&source, statement) != nil {
		return "", false
	}
	projection, ok := shellInterpreterScriptProjection(source.String())
	if !ok || projection.Language != "python" && projection.Language != "javascript" || len(projection.Source) > maxExecProgramBytes {
		return "", false
	}
	python := projection.Language == "python"
	language := codeModeJavaScriptLanguage
	if python {
		language = execPythonLanguage
	}
	deadline := time.Now().Add(execProviderBudget)
	data := []byte(projection.Source)
	tree, err := parseExecSource(data, language, func() bool { return time.Now().After(deadline) })
	if err != nil || tree == nil {
		return "", false
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return "", false
	}
	scan := execSourceScope{input: execProviderInput{deadline: deadline}, source: data, python: python, intentOnly: true,
		vars: make(map[string][]string), texts: make(map[string]bool), assigned: make(map[string]int), aliases: make(map[string]string)}
	scan.walk(tree.RootNode())
	if !scan.writes {
		return "", false
	}
	suffix := " · " + shellInterpreterName(command) + " (requested)"
	var displays []string
	seen := make(map[string]bool)
	for _, scope := range scan.result.scope {
		for _, operand := range scope.Operands {
			if operand.Path != "" && !seen[operand.Path] {
				seen[operand.Path] = true
				displays = append(displays, "Edit "+toolActivityCode(operand.Path)+suffix)
			}
		}
	}
	if len(displays) == 0 || scan.result.open {
		displays = append(displays, "Edit"+suffix)
	}
	return strings.Join(displays, "\n\n"), true
}
