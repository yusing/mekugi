package execscope

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// CreatedScript reads an interpreter's literal input from the immediately
// preceding cat heredoc. It never reads files or evaluates shell source.
// Other commands and control flow leave the ordinary source/fallback unchanged.
func CreatedScript(source string, statement *syntax.Stmt, directory string) (content, path string, ok bool) {
	if statement == nil || len(source) > SourceLimit || !filepath.IsAbs(directory) {
		return "", "", false
	}
	call, simple := statement.Cmd.(*syntax.CallExpr)
	if !simple || len(call.Assigns) != 0 || len(statement.Redirs) != 0 || statement.Background || statement.Coprocess || statement.Disown {
		return "", "", false
	}
	args, literal := shellsyntax.Args(call.Args)
	if !literal || len(args) < 2 {
		return "", "", false
	}
	identity := shellsyntax.InterpreterIdentity(args[0])
	if !strings.HasPrefix(identity, "python") && identity != "node" && identity != "bun" && identity != "deno" {
		return "", "", false
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "")
	if err != nil {
		return "", "", false
	}
	for i, current := range program.Stmts {
		if i == 0 || current.Pos().Offset() != statement.Pos().Offset() {
			continue
		}
		previous := program.Stmts[i-1]
		writer, simple := previous.Cmd.(*syntax.CallExpr)
		if !simple || len(writer.Assigns) != 0 || len(writer.Args) != 1 || len(previous.Redirs) != 2 || previous.Background || previous.Coprocess || previous.Disown || previous.Negated {
			break
		}
		name, _ := shellsyntax.Literal(writer.Args[0])
		if name != "cat" {
			break
		}
		var target, body string
		stdin := false
		for _, redirect := range previous.Redirs {
			if (redirect.Op == syntax.RdrOut || redirect.Op == syntax.RdrClob) && (redirect.N == nil || redirect.N.Value == "1") {
				target, _ = shellsyntax.Literal(redirect.Word)
			} else if redirect.N == nil || redirect.N.Value == "0" {
				body, stdin = shellsyntax.Heredoc(redirect, false)
			}
		}
		if target == "" || !stdin {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(directory, target)
		}
		content, path, reason := readSource(Interpreter{Identity: identity, Arguments: args[1:], Directory: directory}, func(path string) (string, string) {
			if filepath.Clean(path) == filepath.Clean(target) {
				return body, ""
			}
			return "", "different script input"
		})
		return content, path, path != "" && reason == ""
	}
	return "", "", false
}
