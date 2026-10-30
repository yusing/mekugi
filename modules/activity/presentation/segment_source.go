// Source: apps/mekugi/internal/router/exec_track.go:63:103,274:289@[d6415ec1].
package presentation

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// WritesFilesIn retains literal source from the containing shell command when
// a tracked segment executes a script created by an earlier statement.
func (source CommandSource) WritesFilesIn(script string) bool {
	if source.WritesFiles() {
		return true
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return false
	}
	for _, statement := range program.Stmts {
		if strings.TrimSpace(sourceStatementSource(script, statement)) == strings.TrimSpace(source.Script) {
			if text, ok := sourceEditStatement(script, statement); ok && slices.ContainsFunc(Operations(text), func(value Value) bool {
				return slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, value.Verb)
			}) {
				return true
			}
		}
	}
	return false
}

// WritesFiles classifies visible writer intent without establishing effects.
func (source CommandSource) WritesFiles() bool {
	// Activity retains write classification for formatters and file copies.
	// Live previews use the narrower admitted source-segment scope.
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source.Script), "")
	writes := false
	if err == nil {
		syntax.Walk(program, func(node syntax.Node) bool {
			if _, declaration := node.(*syntax.FuncDecl); declaration {
				return false
			}
			if call, ok := node.(*syntax.CallExpr); ok {
				args := call.Args
				for len(args) > 0 {
					name, literal := shellsyntax.Literal(args[0])
					if !literal || filepath.Base(name) != "env" {
						break
					}
					var reason string
					args, reason = shellsyntax.EnvArgs(args[1:])
					if reason != "" {
						break
					}
				}
				if words, literal := shellsyntax.Args(args); literal && len(words) > 0 {
					name := filepath.Base(words[0])
					writes = writes || name == "cp" || name == "install" || execscope.GoFormatterWrites(name, words[1:])
				}
			}
			return !writes
		})
	}
	if writes {
		return true
	}
	for _, block := range Operations(source.SegmentText()) {
		if slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb) {
			return true
		}
	}
	return false
}

// SegmentText uses the same shell classifier as an untracked command.
func (source CommandSource) SegmentText() string {
	if source.Display != "" {
		return ShellSource(source.Display, source.Directory)
	}
	script := source.Script
	if strings.ContainsAny(script, "\r\n") {
		script = source.Display
	}
	return RunSource(script)
}
