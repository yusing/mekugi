package projection

// Source: internal/router/live_diff_shell_write.go:1:242@3e523196, provisional source evaluation.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"mvdan.cc/sh/v3/syntax"
)

// Project only literal heredoc writes; shell execution remains the sole owner
// of actual file effects and durable change evidence.
func liveDiffShellWriteStatement(ctx context.Context, stmt *syntax.Stmt, directory string, partialLine, final bool) ([]diffmodel.ReviewFile, bool, error) {
	if files, recognized, err := liveDiffInterpreterWrite(ctx, stmt, directory, partialLine); recognized || err != nil {
		return files, recognized, err
	}
	if files, recognized, err := liveDiffShellFileOperation(ctx, stmt, directory, partialLine, final); recognized {
		return files, true, err
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated ||
		len(call.Assigns) != 0 || len(call.Args) != 1 || len(stmt.Redirs) != 2 {
		return nil, false, nil
	}
	name, literal := shellsyntax.Literal(call.Args[0])
	if !literal || name != "cat" {
		return nil, false, nil
	}
	var heredoc, output *syntax.Redirect
	for _, redirect := range stmt.Redirs {
		switch redirect.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			heredoc = redirect
		case syntax.RdrOut, syntax.RdrClob, syntax.AppOut:
			if redirect.N != nil && redirect.N.Value != "1" {
				return nil, false, nil
			}
			output = redirect
		default:
			return nil, false, nil
		}
	}
	if heredoc == nil || output == nil {
		return nil, false, nil
	}
	path, literal := shellsyntax.Literal(output.Word)
	content, literalContent := shellsyntax.Heredoc(heredoc, partialLine)
	if !literal || path == "" || !literalContent {
		return nil, false, nil
	}
	if !filepath.IsAbs(path) && !filepath.IsAbs(directory) {
		return nil, true, errors.New("streaming file write requires an absolute execution directory")
	}
	path = shellsyntax.FilePath(directory, path)
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	const limit = FileLimit
	if len(content) > limit || !utf8.ValidString(content) {
		return nil, true, errors.New("streaming file write requires bounded UTF-8 content")
	}
	before, exists, err := liveDiffSourceRead(ctx, path, ReadFile)
	if err != nil {
		return nil, true, err
	}
	beforePath := path
	if !exists {
		beforePath = ""
	}
	if output.Op == syntax.AppOut {
		content = before + content
	}
	if len(content) > limit {
		return nil, true, errors.New("streaming file write result exceeds capacity")
	}
	// An edit script being composed is transport for the intended changes,
	// not the useful diff. Project its supported effects through the same
	// paced preview pipeline; never execute or rewrite the host command.
	if strings.HasSuffix(path, ".py") && output.Op != syntax.AppOut {
		input := previewInterpreterInput{identity: "python", cwd: directory, deadline: time.Now().Add(execscope.Budget)}
		if files, recognized, err := liveDiffInterpreterSource(ctx, input, content, path, true, true); recognized || err != nil {
			return files, recognized, err
		}
		if partialLine && !final && liveDiffPythonSetup(ctx, content) {
			// Imports and path setup cannot yet distinguish an edit script
			// from ordinary Python source. Do not flash that transport source.
			return nil, true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if partialLine && !final && !SourceReady(ctx, path, content) {
		return nil, true, nil
	}
	return []diffmodel.ReviewFile{diffmodel.RenderReviewPreviewFile(beforePath, path, before, content)}, true, nil
}

const FileLimit = 256 << 10

// ReadFile reads bounded UTF-8 content that a preview predicts from.
func ReadFile(path string) (string, bool, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	case !info.Mode().IsRegular() || info.Size() > FileLimit:
		return "", false, errors.New("streaming file preview requires a bounded regular file")
	}
	file, err := sourceparse.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	content, err := sourceparse.ReadText(file, FileLimit, "streaming file preview source exceeds capacity or is not UTF-8")
	return content, err == nil, err
}

// liveDiffShellFileOperation projects tee heredoc content and retains literal
// cp, mv, and rm paths for dependency checks, without previewing their effects.
func liveDiffShellFileOperation(ctx context.Context, stmt *syntax.Stmt, directory string, partialLine, final bool) ([]diffmodel.ReviewFile, bool, error) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated ||
		len(call.Assigns) != 0 || len(call.Args) < 2 {
		return nil, false, nil
	}
	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		word, literal := shellsyntax.Literal(arg)
		if !literal || word == "" {
			return nil, false, nil
		}
		words = append(words, word)
	}
	name := words[0]
	var flags []string
	var operands []string
	for index, word := range words[1:] {
		if word == "--" {
			operands = append(operands, words[index+2:]...)
			break
		}
		if strings.HasPrefix(word, "-") && len(word) > 1 {
			flags = append(flags, word)
			continue
		}
		operands = append(operands, word)
	}
	allowed := map[string]string{"cp": "fp", "mv": "f", "rm": "f", "tee": "a"}[name]
	if allowed == "" {
		return nil, false, nil
	}
	appendOutput := false
	for _, flag := range flags {
		if strings.HasPrefix(flag, "--") || strings.Trim(flag[1:], allowed) != "" {
			return nil, false, nil
		}
		appendOutput = appendOutput || strings.Contains(flag, "a")
	}
	var heredoc *syntax.Redirect
	for _, redirect := range stmt.Redirs {
		switch {
		case name == "tee" && (redirect.Op == syntax.Hdoc || redirect.Op == syntax.DashHdoc):
			heredoc = redirect
		case name == "tee" && redirect.Op == syntax.RdrOut:
			if target, literal := shellsyntax.Literal(redirect.Word); !literal || target != "/dev/null" {
				return nil, false, nil
			}
		default:
			return nil, false, nil
		}
	}
	if len(operands) == 0 || name == "tee" && heredoc == nil || (name == "cp" || name == "mv") && len(operands) != 2 {
		return nil, false, nil
	}
	// A partial final word could still grow into another path.
	if partialLine && heredoc == nil {
		return nil, false, nil
	}
	if !filepath.IsAbs(directory) {
		return nil, true, errors.New("streaming file preview requires an absolute execution directory")
	}
	for index := range operands {
		operands[index] = filepath.Clean(shellsyntax.FilePath(directory, operands[index]))
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	switch name {
	case "rm":
		files := make([]diffmodel.ReviewFile, 0, len(operands))
		for _, path := range operands {
			files = append(files, diffmodel.ReviewFile{BeforePath: path})
		}
		return files, true, nil
	case "tee":
		content, literal := shellsyntax.Heredoc(heredoc, partialLine)
		if !literal || len(content) > FileLimit || !utf8.ValidString(content) {
			return nil, true, errors.New("streaming file write requires bounded UTF-8 content")
		}
		files := make([]diffmodel.ReviewFile, 0, len(operands))
		for _, path := range operands {
			before, exists, err := liveDiffSourceRead(ctx, path, ReadFile)
			if err != nil {
				return nil, true, err
			}
			after, beforePath := content, path
			if appendOutput {
				after = before + content
			}
			if !exists {
				beforePath = ""
			}
			if partialLine && !final && (!liveDiffSourceTerminated(ctx, path, content) || !SourceComplete(ctx, path, after)) {
				return nil, true, nil
			}
			files = append(files, diffmodel.RenderReviewPreviewFile(beforePath, path, before, after))
		}
		return files, true, nil
	}
	source, target := operands[0], operands[1]
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, filepath.Base(source))
	}
	// File operations constrain dependent predictions, but carry no arriving
	// source content. Keep their paths without reading or projecting bytes.
	if name == "cp" {
		return []diffmodel.ReviewFile{{AfterPath: target}}, true, nil
	}
	return []diffmodel.ReviewFile{{BeforePath: source, AfterPath: target}}, true, nil
}
