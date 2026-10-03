package router

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Temporary roots are capabilities, not guessed filenames or resolved paths.
// NUL keeps a symbolic mktemp result distinct from any real filesystem operand.
// This proof is used only by the activity intent scanner, never by capture.
func toolActivityTemporaryEnv(script string, before *syntax.Stmt) map[string]string {
	end := int(before.Pos().Offset())
	if end <= 0 || end > len(script) || end > maxExecProgramBytes {
		return nil
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(script[:end]), "")
	if err != nil {
		return nil
	}
	vars, environment := make(map[string]string), make(map[string]string)
	for _, statement := range program.Stmts {
		if statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
			return nil
		}
		switch command := statement.Cmd.(type) {
		case *syntax.CallExpr:
			if len(command.Args) != 0 {
				name, literal := shellCatLiteral(command.Args[0])
				if !literal || interp.IsBuiltin(name) || len(command.Assigns) != 0 {
					return nil
				}
				for _, arg := range command.Args[1:] {
					if _, literal := shellCatLiteral(arg); !literal {
						return nil
					}
				}
				for _, redirect := range statement.Redirs {
					if redirect.Op != syntax.RdrOut && redirect.Op != syntax.AppOut && redirect.Op != syntax.RdrClob || !toolActivityPatternWord(redirect.Word) || redirect.N != nil && !isDigits(redirect.N.Value) {
						return nil
					}
				}
				// A literal external call cannot rebind its parent shell's
				// variables. Simple output paths have no expansion effects.
				continue
			}
			if len(statement.Redirs) != 0 {
				return nil
			}
			for _, assignment := range command.Assigns {
				if assignment.Name == nil || assignment.Append || assignment.Array != nil || assignment.Index != nil {
					return nil
				}
				name := assignment.Name.Value
				if name == "PATH" || name == "BASH_ENV" || name == "ENV" {
					return nil
				}
				vars[name] = toolActivityTemporaryValue(assignment.Value, vars, name)
				if vars[name] == "" {
					if _, literal := shellCatLiteral(assignment.Value); !literal {
						return nil
					}
				}
				if _, exported := environment[name]; exported {
					environment[name] = vars[name]
				}
			}
		case *syntax.DeclClause:
			if command.Variant.Value != "export" || len(statement.Redirs) != 0 {
				return nil
			}
			for _, assignment := range command.Args {
				if assignment.Name == nil || assignment.Append || assignment.Array != nil || assignment.Index != nil {
					return nil
				}
				name := assignment.Name.Value
				if name == "PATH" || name == "BASH_ENV" || name == "ENV" {
					return nil
				}
				if assignment.Value != nil {
					vars[name] = toolActivityTemporaryValue(assignment.Value, vars, name)
					if vars[name] == "" {
						if _, literal := shellCatLiteral(assignment.Value); !literal {
							return nil
						}
					}
				}
				environment[name] = vars[name]
			}
		default:
			return nil
		}
	}
	for name, root := range environment {
		if root == "" {
			delete(environment, name)
		}
	}
	return environment
}

func toolActivityTemporaryValue(word *syntax.Word, vars map[string]string, name string) string {
	if word == nil || len(word.Parts) != 1 {
		return ""
	}
	part := word.Parts[0]
	if quoted, ok := part.(*syntax.DblQuoted); ok {
		if len(quoted.Parts) != 1 {
			return ""
		}
		part = quoted.Parts[0]
	}
	if parameter, ok := part.(*syntax.ParamExp); ok && parameter.Param != nil && !parameter.Excl && !parameter.Length && !parameter.Width && parameter.Index == nil && parameter.Slice == nil && parameter.Repl == nil && parameter.Exp == nil {
		return vars[parameter.Param.Value]
	}
	substitution, ok := part.(*syntax.CmdSubst)
	if !ok || substitution.Backquotes || len(substitution.Stmts) != 1 {
		return ""
	}
	statement := substitution.Stmts[0]
	if statement.Background || statement.Negated || len(statement.Redirs) != 0 {
		return ""
	}
	args, ok := toolActivityLiteralCall(statement)
	if !ok || len(args) != 3 || args[0] != "mktemp" || args[1] != "-d" {
		return ""
	}
	// Only an explicit, absolute template in a system temporary directory
	// establishes this capability. Do not trust inherited TMPDIR or -p.
	template := args[2]
	parent := filepath.Dir(template)
	if parent != "/tmp" && parent != "/var/tmp" || !strings.HasSuffix(template, "XXX") {
		return ""
	}
	return "/\x00temporary/" + name
}
