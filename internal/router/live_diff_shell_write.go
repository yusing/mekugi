package router

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/syntax"
)

// Project only literal heredoc writes; shell execution remains the sole owner
// of actual file effects and durable change evidence.
func liveDiffShellWriteStatement(ctx context.Context, stmt *syntax.Stmt, directory string, partialLine bool) ([]mekugi.ReviewFile, bool, error) {
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
	const limit = 256 << 10
	if len(content) > limit || !utf8.ValidString(content) {
		return nil, true, errors.New("streaming file write requires bounded UTF-8 content")
	}
	beforePath, before := path, ""
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		beforePath = ""
	case err != nil:
		return nil, true, err
	case !info.Mode().IsRegular() || info.Size() > limit:
		return nil, true, errors.New("streaming file write requires a bounded regular file")
	default:
		file, err := os.Open(path)
		if err != nil {
			return nil, true, err
		}
		data, err := io.ReadAll(io.LimitReader(file, limit+1))
		file.Close()
		if err != nil {
			return nil, true, err
		}
		if len(data) > limit || !utf8.Valid(data) {
			return nil, true, errors.New("streaming file write source exceeds capacity or is not UTF-8")
		}
		before = string(data)
	}
	if output.Op == syntax.AppOut {
		content = before + content
	}
	if len(content) > limit {
		return nil, true, errors.New("streaming file write result exceeds capacity")
	}
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	return []mekugi.ReviewFile{mekugi.RenderReviewFile(beforePath, path, before, content)}, true, nil
}
