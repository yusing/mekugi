package presentation

// Source: apps/mekugi/internal/router/subagent_tool_display_edit.go:1:128@0a356401.

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"mvdan.cc/sh/v3/syntax"
)

// Intent is not a receipt. Keep edit source out of Run cards before capture,
// without reading target files or claiming the requested writes succeeded.
func sourceEditStatement(script string, statement *syntax.Stmt) (string, bool) {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Negated || statement.Coprocess || statement.Disown || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return "", false
	}
	command, literal := shellsyntax.Literal(call.Args[0])
	if !literal {
		return "", false
	}
	name := filepath.Base(command)
	if (name == "sed" || name == "rm" || name == "mv") && len(statement.Redirs) == 0 {
		// Reuse capture's option/script parser without resolving or reading
		// files. The operation is requested; only its later receipt has counts.
		plan := execscope.StatementIntent(statement)
		verb := "Edit"
		switch name {
		case "rm":
			verb = "Delete"
		case "mv":
			verb = "Move"
		}
		if plan.Class != execscope.Declared || len(plan.Scope) == 0 {
			return "", false
		}
		var paths []string
		for _, scope := range plan.Scope {
			for _, operand := range scope.Operands {
				path := operand.Path
				if name == "mv" {
					destination := scope.Dest
					if scope.DestDir {
						destination = filepath.Join(destination, filepath.Base(path))
					}
					path = pathdisplay.Move("", path, destination)
				}
				paths = append(paths, verb+" "+SourceCode(path)+" · "+name+" (requested)")
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
			path, literal := shellsyntax.Literal(redirect.Word)
			if !literal && shellsyntax.PatternWord(redirect.Word) {
				path = shellWords([]*syntax.Word{redirect.Word})
			}
			if path != "" && !strings.HasPrefix(path, "/dev/") {
				paths = append(paths, "Edit "+SourceCode(path)+" · cat (requested)")
			}
		}
		if len(paths) != 0 {
			return strings.Join(paths, "\n\n"), true
		}
	}
	languageName := sourceLanguage(command)
	if languageName != "python" && languageName != "javascript" {
		return "", false
	}
	projection, ok := shellStatementProjection(statement, false)
	if !ok {
		if content, _, created := execscope.CreatedScript(script, statement, "/"); created {
			projection = shellScriptProjection{Source: content, Language: languageName, Via: shellViaWords("", call.Args)}
			ok = true
		}
	}
	if !ok || projection.Language != "python" && projection.Language != "javascript" || len(projection.Source) > execscope.SourceLimit {
		return "", false
	}
	python := projection.Language == "python"
	language := sourceparse.JavaScript
	if python {
		language = sourceparse.Python
	}
	deadline := time.Now().Add(execscope.Budget)
	data := []byte(projection.Source)
	tree, err := sourceparse.Parse(data, language, func() bool { return time.Now().After(deadline) })
	if err != nil || tree == nil {
		return "", false
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return "", false
	}
	scan := execscope.NewBindings(execscope.SourceInput{Source: data, Deadline: deadline, Python: python, IntentOnly: true, TemporaryEnv: sourceTemporaryEnv(script, statement)})
	result := scan.Analyze(tree.RootNode())
	if !result.Writes {
		return "", false
	}
	if len(result.Scope) == 0 {
		// Unresolved targets have no edit to open. Keep the ordinary Run
		// projection so its dialog retains the original command and output.
		return "", false
	}
	suffix := " · " + shellsyntax.InterpreterIdentity(command) + " (requested)"
	var displays []string
	seen := make(map[string]bool)
	for _, scope := range result.Scope {
		for _, operand := range scope.Operands {
			if operand.Path != "" && !seen[operand.Path] {
				seen[operand.Path] = true
				displays = append(displays, "Edit "+SourceCode(operand.Path)+suffix)
			}
		}
	}
	if result.Unresolved {
		display, ok := projection.display()
		if !ok {
			return "", false
		}
		displays = append(displays, display)
	}
	return strings.Join(displays, "\n\n"), true
}
