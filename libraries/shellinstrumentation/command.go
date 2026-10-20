package shellinstrumentation

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Source: backends/codex/binding/command_observation.go:51:90@45fbe279
// CommandScript is the script of a host Bash or sh command, exactly as the
// shell receives it with -c or -lc.
func CommandScript(command string) (string, bool) {
	args, ok := CommandArgs(command)
	if !ok || len(args) != 3 || args[1] != "-lc" && args[1] != "-c" {
		return "", false
	}
	name := filepath.Base(args[0])
	name = strings.TrimSuffix(name, filepath.Ext(name))
	return ShOriginal(args[2]), name == "bash" || name == "sh" || name == "dash"
}

// CommandArgs unquotes a host command made only of literal words.
func CommandArgs(command string) ([]string, bool) {
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil, false
	}
	stmt := program.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 3 {
		return nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if !shellsyntax.LiteralParts(word.Parts, false) {
			return nil, false
		}
		// Fields removes shell quoting without consulting the environment or
		// executing substitutions. Reject words that expand to multiple args.
		fields, err := expand.Fields(&expand.Config{}, word)
		if err != nil || len(fields) != 1 {
			return nil, false
		}
		args = append(args, fields[0])
	}
	return args, true
}
