package router

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/syntax"
)

// Project only literal heredoc writes; shell execution remains the sole owner
// of actual file effects and durable change evidence.
func liveDiffShellWriteStatement(ctx context.Context, stmt *syntax.Stmt, directory string, partialLine, final bool) ([]mekugi.ReviewFile, bool, error) {
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
	name, literal := shellCatLiteral(call.Args[0])
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
	path, literal := shellCatLiteral(output.Word)
	content, literalContent := liveDiffShellHeredoc(heredoc, partialLine)
	if !literal || path == "" || !literalContent {
		return nil, false, nil
	}
	if !filepath.IsAbs(path) && !filepath.IsAbs(directory) {
		return nil, true, errors.New("streaming file write requires an absolute execution directory")
	}
	path = shellFilePath(directory, path)
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	const limit = liveDiffPreviewFileLimit
	if len(content) > limit || !utf8.ValidString(content) {
		return nil, true, errors.New("streaming file write requires bounded UTF-8 content")
	}
	before, exists, err := liveDiffSourceRead(ctx, path, liveDiffPreviewFile)
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
		input := execProviderInput{identity: "python", cwd: directory, deadline: time.Now().Add(execProviderBudget)}
		if files, recognized, err := liveDiffInterpreterSource(ctx, input, content, path, true, true); recognized || err != nil {
			return files, recognized, err
		}
		if partialLine && !final {
			// Imports and path setup cannot yet distinguish an edit script
			// from ordinary Python source. Do not flash that transport source.
			return nil, true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if partialLine && !final && !liveDiffSourceReady(ctx, path, content) {
		return nil, true, nil
	}
	return []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, path, before, content)}, true, nil
}

const liveDiffPreviewFileLimit = 256 << 10

// liveDiffPreviewFile reads bounded UTF-8 content that a preview predicts from.
func liveDiffPreviewFile(path string) (string, bool, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	case !info.Mode().IsRegular() || info.Size() > liveDiffPreviewFileLimit:
		return "", false, errors.New("streaming file preview requires a bounded regular file")
	}
	file, err := openNativePatchFile(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, liveDiffPreviewFileLimit+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > liveDiffPreviewFileLimit || !utf8.Valid(data) {
		return "", false, errors.New("streaming file preview source exceeds capacity or is not UTF-8")
	}
	return string(data), true, nil
}

// liveDiffShellFileOperation predicts literal cp, mv, rm, and tee heredoc
// effects from current files. The prediction is display only; the command's
// observed record is the evidence.
func liveDiffShellFileOperation(ctx context.Context, stmt *syntax.Stmt, directory string, partialLine, final bool) ([]mekugi.ReviewFile, bool, error) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated ||
		len(call.Assigns) != 0 || len(call.Args) < 2 {
		return nil, false, nil
	}
	words := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		word, literal := shellCatLiteral(arg)
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
			if target, literal := shellCatLiteral(redirect.Word); !literal || target != "/dev/null" {
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
		operands[index] = filepath.Clean(shellFilePath(directory, operands[index]))
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	switch name {
	case "rm":
		files := make([]mekugi.ReviewFile, 0, len(operands))
		for _, path := range operands {
			before, exists, err := liveDiffSourceRead(ctx, path, liveDiffPreviewFile)
			if err != nil {
				return nil, true, err
			}
			if exists {
				files = append(files, mekugi.RenderReviewFile(path, "", before, ""))
			}
		}
		return files, true, nil
	case "tee":
		content, literal := liveDiffShellHeredoc(heredoc, partialLine)
		if !literal || len(content) > liveDiffPreviewFileLimit || !utf8.ValidString(content) {
			return nil, true, errors.New("streaming file write requires bounded UTF-8 content")
		}
		files := make([]mekugi.ReviewFile, 0, len(operands))
		for _, path := range operands {
			before, exists, err := liveDiffSourceRead(ctx, path, liveDiffPreviewFile)
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
			if partialLine && !final && (!liveDiffSourceTerminated(ctx, path, content) || !liveDiffSourceComplete(ctx, path, after)) {
				return nil, true, nil
			}
			files = append(files, mekugi.RenderReviewFile(beforePath, path, before, after))
		}
		return files, true, nil
	}
	source, target := operands[0], operands[1]
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, filepath.Base(source))
	}
	content, exists, err := liveDiffSourceRead(ctx, source, liveDiffPreviewFile)
	if err != nil {
		return nil, true, err
	}
	if !exists {
		return nil, true, errors.New("streaming file preview source does not exist")
	}
	before, targetExists, err := liveDiffSourceRead(ctx, target, liveDiffPreviewFile)
	if err != nil {
		return nil, true, err
	}
	if name == "cp" {
		beforePath := target
		if !targetExists {
			beforePath = ""
		}
		file := mekugi.RenderReviewFile(beforePath, target, before, content)
		if !targetExists {
			file.CopyFrom = source
		}
		return []mekugi.ReviewFile{file}, true, nil
	}
	if targetExists {
		// A replacing move deletes the target's content, which one move
		// entry cannot show.
		return []mekugi.ReviewFile{
			mekugi.RenderReviewFile(source, "", content, ""),
			mekugi.RenderReviewFile(target, target, before, content),
		}, true, nil
	}
	return []mekugi.ReviewFile{mekugi.RenderReviewFile(source, target, content, content)}, true, nil
}
