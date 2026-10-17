package projection

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"mvdan.cc/sh/v3/syntax"
)

// Shell predicts literal writes without executing source. Recognized reports
// edit intent even when its projection cannot be established.
func Shell(ctx context.Context, program, directory string, final bool) ([]diffmodel.ReviewFile, string, bool, error) {
	statements, directory, partialLine, parsed := shellsyntax.StreamingStatements(program, directory)
	if !final && !partialLine {
		// A normal command's final operand may still be arriving. An open
		// heredoc instead gates its decoded target content inside projection.
		program = program[:strings.LastIndexByte(program, '\n')+1]
		statements, directory, partialLine, parsed = shellsyntax.StreamingStatements(program, directory)
	}
	if !parsed {
		return nil, "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var files []diffmodel.ReviewFile
	var tools []string
	recognized := false
	seenPaths := make(map[string]struct{})
	tainted := false
	// A preview assumes each step of an `a && b` chain succeeds, as it does
	// for separate statements.
	statements = shellsyntax.Sequence(statements)
	for index, stmt := range statements {
		if next, ok := shellsyntax.LiteralCd(stmt, directory); ok {
			directory = next
			continue
		}
		statementPartial := partialLine && index == len(statements)-1
		projected, ok, err := liveDiffShellWriteStatement(ctx, stmt, directory, statementPartial, final)
		if !ok {
			if !ShellNeutral(stmt) {
				tainted = true
			}
			continue
		}
		recognized = true
		if tainted {
			return nil, "", true, errors.New("edit follows unsupported shell state")
		}
		if err != nil {
			return nil, "", true, err
		}
		if statementPartial && !final && len(projected) == 0 {
			// Keep all previously displayed files while this statement waits
			// for source, rather than publishing only its completed siblings.
			return nil, "", true, nil
		}
		operationPaths := make(map[string]struct{})
		for _, file := range projected {
			if file.BeforePath != "" {
				operationPaths[file.BeforePath] = struct{}{}
			}
			if file.AfterPath != "" {
				operationPaths[file.AfterPath] = struct{}{}
			}
		}
		for path := range operationPaths {
			if _, exists := seenPaths[path]; exists {
				return nil, "", true, errors.New("edits depend on an earlier operation")
			}
		}
		for path := range operationPaths {
			seenPaths[path] = struct{}{}
		}
		// Path-only operations constrain later predictions without arriving
		// source content. No-op content writes likewise have no hunk to show.
		projected = slices.DeleteFunc(projected, func(file diffmodel.ReviewFile) bool { return file.Diff == "" })
		files = append(files, projected...)
		if len(projected) > 0 {
			if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
				if name, literal := shellsyntax.Literal(call.Args[0]); literal {
					name = shellsyntax.InterpreterIdentity(name)
					if !slices.Contains(tools, name) {
						tools = append(tools, name)
					}
				}
			}
		}
	}
	return files, strings.Join(tools, "+"), recognized, nil
}
