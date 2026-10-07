package execsegment

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// ClaudeWrapper decodes only the literal eval operand of the native shell's
// setup/eval/cwd-write wrapper. Setup and the native cwd write stay unchanged.
// Unsupported wrappers run untracked, not through a substitute executor.
func ClaudeWrapper(wrapper string) (script, rewritten string, segments []Segment, ok bool) {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(wrapper), "")
	if err != nil || len(program.Stmts) != 1 || !trapsFirst(program.Stmts) {
		return
	}
	var leaves []*syntax.Stmt
	var flatten func(*syntax.Stmt)
	flatten = func(stmt *syntax.Stmt) {
		if binary, isBinary := stmt.Cmd.(*syntax.BinaryCmd); isBinary && binary.Op == syntax.AndStmt && !stmt.Negated && len(stmt.Redirs) == 0 {
			flatten(binary.X)
			flatten(binary.Y)
		} else {
			leaves = append(leaves, stmt)
		}
	}
	flatten(program.Stmts[0])
	if len(leaves) < 2 {
		return
	}
	last := leaves[len(leaves)-1]
	pwd, isCall := last.Cmd.(*syntax.CallExpr)
	if !isCall || last.Negated || last.Background || len(pwd.Assigns) != 0 || len(pwd.Args) != 2 || pwd.Args[0].Lit() != "pwd" || pwd.Args[1].Lit() != "-P" || len(last.Redirs) != 1 {
		return
	}
	redirect := last.Redirs[0]
	if redirect.Op != syntax.ClbOut || redirect.N != nil || redirect.Hdoc != nil {
		return
	}
	path, literal := claudeLiteral(redirect.Word)
	base := filepath.Base(path)
	if !literal || !filepath.IsAbs(path) || !(strings.HasPrefix(base, "claude-") && strings.HasSuffix(base, "-cwd") || strings.HasPrefix(base, "cwd-")) {
		return
	}
	stmt := leaves[len(leaves)-2]
	call, isCall := stmt.Cmd.(*syntax.CallExpr)
	if !isCall || stmt.Negated || stmt.Background || len(call.Assigns) != 0 || len(call.Args) != 2 || call.Args[0].Lit() != "eval" {
		return
	}
	// The native wrapper may redirect eval's stdin to /dev/null only.
	if len(stmt.Redirs) > 1 {
		return
	}
	if len(stmt.Redirs) == 1 {
		r := stmt.Redirs[0]
		input, literal := claudeLiteral(r.Word)
		if r.Op != syntax.RdrIn || r.N != nil || r.Hdoc != nil || !literal || input != "/dev/null" {
			return
		}
	}
	script, literal = claudeLiteral(call.Args[1])
	if !literal {
		return
	}
	segments, ok = Split(script)
	if !ok {
		return
	}
	operand := call.Args[1]
	rewritten = wrapper[:operand.Pos().Offset()] + shellsyntax.Quote(Rewrite(script, segments)) + wrapper[operand.End().Offset():]
	return
}

// Reject expansions before using the syntax library's literal decoder. No
// environment, command, tilde, glob or filesystem expansion is performed.
func claudeLiteral(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				if _, literal := inner.(*syntax.Lit); !literal {
					return "", false
				}
			}
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, "~*?[]{}") {
				return "", false
			}
		default:
			return "", false
		}
	}
	values, err := expand.Fields(&expand.Config{}, word)
	if err != nil || len(values) != 1 {
		return "", false
	}
	return values[0], true
}
