// Package execsegment splits a Codex command script into its top-level
// segments and instruments them for per-segment tracking. The helper that
// runs inside the command shell and the router that displays it share this
// split, so their segment indices always agree.
package execsegment

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Segment is one top-level command of a list: a statement, or an operand of a
// top-level && or || chain. Pipelines and compound commands stay whole.
type Segment struct {
	Start, End int    // Script byte span, excluding terminators and heredoc bodies.
	Source     string // Display source, including any heredoc body.
}

// Hook names are part of the rewritten script's contract with the shell hook.
const (
	beginHook = "__mekugi_b"
	endHook   = "__mekugi_e"
)

// Split returns the script's segments when every segment can be tracked
// without changing the script's behavior. It reports false for single
// commands, for scripts whose first command runs before the shell's DEBUG
// trap can replace the script, and for scripts whose semantics depend on the
// shell state the instrumentation touches: job control, traps, top-level
// returns, process replacement, command tracing, and variables naming the
// current command.
func Split(script string) ([]Segment, bool) {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil || strings.Contains(script, "__mekugi_") || !trapsFirst(program.Stmts) || !trackable(program) {
		return nil, false
	}
	var segments []Segment
	var add func(*syntax.Stmt)
	add = func(stmt *syntax.Stmt) {
		if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && (binary.Op == syntax.AndStmt || binary.Op == syntax.OrStmt) &&
			!stmt.Negated && len(stmt.Redirs) == 0 {
			add(binary.X)
			add(binary.Y)
			return
		}
		start, end := int(stmt.Pos().Offset()), int(stmtEnd(stmt).Offset())
		segments = append(segments, Segment{Start: start, End: end, Source: shellsyntax.StatementSource(stmt, script[start:end])})
	}
	for _, stmt := range program.Stmts {
		add(stmt)
	}
	if len(segments) < 2 {
		return nil, false
	}
	// The rewrite must parse, or the shell would reject a script that the
	// user's own shell accepts.
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(Rewrite(script, segments)), ""); err != nil {
		return nil, false
	}
	return segments, true
}

// Rewrite wraps each segment in hooks that report its start and exit status.
// Each wrapper runs in the current shell and returns the segment's status,
// so && and || chains, $?, and shell state behave as in the original. A hook
// passes a nonzero status on as the operand of an && list, where errexit
// ignores it: under set -e, only the segment's own command can end the shell.
func Rewrite(script string, segments []Segment) string {
	var out strings.Builder
	offset := 0
	for i, segment := range segments {
		out.WriteString(script[offset:segment.Start])
		fmt.Fprintf(&out, "{ %s %d && :; %s; %s %d && :; }", beginHook, i, script[segment.Start:segment.End], endHook, i)
		offset = segment.End
	}
	out.WriteString(script[offset:])
	return out.String()
}

// stmtEnd ends a statement at its command or last redirection word, before
// any terminator and before heredoc bodies, which follow the line.
func stmtEnd(stmt *syntax.Stmt) syntax.Pos {
	end := stmt.Position
	later := func(pos syntax.Pos) {
		if pos.Offset() > end.Offset() {
			end = pos
		}
	}
	switch cmd := stmt.Cmd.(type) {
	case nil:
	case *syntax.BinaryCmd:
		later(stmtEnd(cmd.Y))
	case *syntax.TimeClause:
		if cmd.Stmt == nil {
			later(cmd.End())
		} else {
			later(stmtEnd(cmd.Stmt))
		}
	default:
		later(cmd.End())
	}
	for _, redirect := range stmt.Redirs {
		if redirect.Hdoc != nil {
			later(redirect.Word.End())
		} else {
			later(redirect.End())
		}
	}
	return end
}

// trapsFirst reports whether the script's first command runs in the shell
// itself, where the DEBUG trap that starts the instrumented copy fires before
// the command has any effect. A first subshell or timed command runs before
// the trap, and would run again inside the copy.
func trapsFirst(stmts []*syntax.Stmt) bool {
	for _, stmt := range stmts {
		switch cmd := stmt.Cmd.(type) {
		case *syntax.FuncDecl:
			continue // Only defines a function, which the copy defines again.
		case nil, *syntax.CallExpr, *syntax.DeclClause, *syntax.LetClause, *syntax.TestClause, *syntax.ArithmCmd, *syntax.ForClause, *syntax.CaseClause:
			return true
		case *syntax.BinaryCmd:
			return trapsFirst([]*syntax.Stmt{cmd.X})
		case *syntax.Block:
			return trapsFirst(cmd.Stmts)
		case *syntax.IfClause:
			return trapsFirst(cmd.Cond)
		case *syntax.WhileClause:
			return trapsFirst(cmd.Cond)
		}
		return false
	}
	return false
}

// Builtins whose effect depends on the shell's job table, traps, or frame,
// all of which the instrumentation shares with the script.
var untrackedBuiltins = []string{"wait", "jobs", "fg", "bg", "disown", "trap", "coproc", "suspend", "caller"}

// Variables the instrumentation changes between segments.
var untrackedParameters = []string{"PIPESTATUS", "_", "LINENO", "BASH_LINENO", "BASH_COMMAND", "BASH_SOURCE", "FUNCNAME", "BASH_EXECUTION_STRING"}

func trackable(program *syntax.File) bool {
	ok := true
	var visit func(node syntax.Node, function bool) bool
	visit = func(node syntax.Node, function bool) bool {
		switch node := node.(type) {
		case *syntax.Stmt:
			if node.Background || node.Coprocess || node.Disown {
				ok = false
			}
		case *syntax.FuncDecl:
			if node.Body != nil {
				syntax.Walk(node.Body, func(child syntax.Node) bool { return ok && visit(child, true) })
			}
			return false
		case *syntax.CoprocClause:
			ok = false
		case *syntax.ParamExp:
			if node.Param != nil && slices.Contains(untrackedParameters, node.Param.Value) {
				ok = false
			}
		case *syntax.CallExpr:
			if len(node.Args) == 0 {
				break
			}
			name := node.Args[0].Lit()
			switch {
			case slices.Contains(untrackedBuiltins, name):
				ok = false
			case name == "return" && !function:
				ok = false
			case name == "exec" && len(node.Args) > 1:
				ok = false // Redirection-only exec has no arguments.
			case name == "set" || name == "shopt":
				ok = !tracing(node)
			}
		}
		return ok
	}
	syntax.Walk(program, func(node syntax.Node) bool { return ok && visit(node, false) })
	return ok
}

// tracing reports shell options that print the commands the shell runs,
// which would print the instrumentation into the command's own output.
func tracing(call *syntax.CallExpr) bool {
	for _, word := range call.Args[1:] {
		arg := word.Lit()
		if arg == "" {
			return true // Unknown options cannot be ruled out.
		}
		if strings.Contains(arg, "xtrace") || strings.Contains(arg, "verbose") || strings.Contains(arg, "extdebug") || strings.Contains(arg, "functrace") {
			return true
		}
		if (strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+")) && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg[1:], "xvT") {
			return true
		}
	}
	return false
}
