package router

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi"
)

// projectStockPatchPreview follows the former hpatch preview model: read the
// bounded source, calculate a disposable in-memory result, and hand the real
// before/after pair to the shared review renderer. It never writes a file or
// treats a speculative projection as evidence of host execution.
func projectStockPatchPreview(ctx context.Context, workspace string, preview liveDiffPreview) liveDiffPreview {
	const limit = 256 << 10
	if len(preview.Input) > limit {
		preview.Input = ""
		preview.Status = liveDiffPreviewUnavailable + "patch exceeds projection capacity"
		return preview
	}
	files, err := stockPatchReviewPreview(ctx, workspace, preview.Input)
	preview.Input = ""
	if err != nil {
		preview.Status = liveDiffPreviewUnavailable + "patch cannot be projected"
	} else {
		preview.Files = files
		if len(files) == 0 {
			// Keep a visible provisional card before the first complete change.
			preview.Input = "\n"
		} else {
			preview.DiffText = false // ReviewFiles use the normal language-aware renderer.
		}
	}
	return preview
}

type stockPreviewFile struct {
	beforePath, afterPath string
	before, after         string
	operation             byte
	chunks                []stockPreviewChunk
}

type stockPreviewChunk struct {
	context   string
	old, new  []string
	endOfFile bool
}

func stockPatchReviewPreview(ctx context.Context, workspace, input string) ([]mekugi.ReviewFile, error) {
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) == 0 || strings.TrimSuffix(lines[0], "\r") != "*** Begin Patch" {
		return nil, errors.New("missing patch start")
	}
	resolve := func(path string) (string, error) {
		if path == "" || strings.ContainsRune(path, 0) {
			return "", errors.New("invalid path")
		}
		if !filepath.IsAbs(path) {
			if !filepath.IsAbs(workspace) {
				return "", errors.New("missing workspace")
			}
			path = filepath.Join(workspace, path)
		}
		return filepath.Clean(path), nil
	}
	var edits []stockPreviewFile
	seenPaths := make(map[string]bool)
	current := -1
	for _, line := range lines[1:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line = strings.TrimSuffix(line, "\r")
		if line == "*** End Patch" {
			break
		}
		var op byte
		var path string
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			op, path = 'a', strings.TrimPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "*** Delete File: "):
			op, path = 'd', strings.TrimPrefix(line, "*** Delete File: ")
		case strings.HasPrefix(line, "*** Update File: "):
			op, path = 'u', strings.TrimPrefix(line, "*** Update File: ")
		}
		if op != 0 {
			resolved, err := resolve(path)
			if err != nil {
				return nil, err
			}
			if seenPaths[resolved] {
				return nil, errors.New("dependent patch operations cannot be projected")
			}
			seenPaths[resolved] = true
			edit := stockPreviewFile{operation: op, afterPath: resolved}
			if op != 'a' {
				edit.beforePath = resolved
				var exists bool
				edit.before, exists, err = liveDiffSourceRead(ctx, resolved, readNativePatchFile)
				if err != nil || !exists || len(edit.before) > 256<<10 {
					return nil, errors.New("source unavailable for bounded preview")
				}
				edit.after = edit.before
			} else {
				_, exists, readErr := liveDiffSourceRead(ctx, resolved, readNativePatchFile)
				if readErr != nil || exists {
					return nil, errors.New("add target unavailable for preview")
				}
			}
			if op == 'd' {
				edit.afterPath = ""
			}
			edits = append(edits, edit)
			current = len(edits) - 1
			continue
		}
		if current < 0 {
			continue // An unfinished envelope has no file to project yet.
		}
		edit := &edits[current]
		switch {
		case edit.operation == 'a' && strings.HasPrefix(line, "+"):
			edit.after += line[1:] + "\n"
		case edit.operation == 'u' && strings.HasPrefix(line, "*** Move to: "):
			if len(edit.chunks) != 0 {
				return nil, errors.New("move appears after update content")
			}
			var err error
			edit.afterPath, err = resolve(strings.TrimPrefix(line, "*** Move to: "))
			if err != nil {
				return nil, err
			}
			if seenPaths[edit.afterPath] {
				return nil, errors.New("dependent patch operations cannot be projected")
			}
			_, exists, readErr := liveDiffSourceRead(ctx, edit.afterPath, readNativePatchFile)
			if readErr != nil || exists {
				return nil, errors.New("move target unavailable for preview")
			}
			seenPaths[edit.afterPath] = true
		case edit.operation == 'u' && strings.HasPrefix(line, "@@"):
			edit.chunks = append(edit.chunks, stockPreviewChunk{context: strings.TrimPrefix(line, "@@ ")})
			if line == "@@" {
				edit.chunks[len(edit.chunks)-1].context = ""
			}
		case edit.operation == 'u' && line == "*** End of File":
			if len(edit.chunks) > 0 {
				edit.chunks[len(edit.chunks)-1].endOfFile = true
			}
		case edit.operation == 'u' && (line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			if len(edit.chunks) == 0 {
				edit.chunks = append(edit.chunks, stockPreviewChunk{})
			}
			chunk := &edit.chunks[len(edit.chunks)-1]
			if line == "" {
				chunk.old = append(chunk.old, "")
				chunk.new = append(chunk.new, "")
				continue
			}
			if line[0] != '+' {
				chunk.old = append(chunk.old, line[1:])
			}
			if line[0] != '-' {
				chunk.new = append(chunk.new, line[1:])
			}
		default:
			return nil, errors.New("unsupported patch line")
		}
	}
	// The file still arriving ends at its streamed tip. Later source is not
	// yet context: the next patch line may remove it, and trailing context
	// would renumber under every added row.
	partial := !slices.ContainsFunc(lines, func(line string) bool { return strings.TrimSuffix(line, "\r") == "*** End Patch" })
	var reviews []mekugi.ReviewFile
	for index, edit := range edits {
		if edit.operation == 'u' {
			after, beforeTip, afterTip, err := projectStockUpdate(ctx, edit.before, edit.chunks)
			if err != nil {
				return nil, err
			}
			edit.after = after
			if partial && index == len(edits)-1 && len(edit.chunks) != 0 {
				edit.before, edit.after = liveDiffLinePrefix(edit.before, beforeTip), liveDiffLinePrefix(after, afterTip)
			}
		}
		if edit.operation == 'd' {
			edit.after = ""
		}
		if edit.before != edit.after || edit.beforePath != edit.afterPath {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			reviews = append(reviews, mekugi.RenderReviewFile(edit.beforePath, edit.afterPath, edit.before, edit.after))
		}
	}
	return reviews, nil
}

// Only exact, ordered source matches are projected. Codex may accept more
// flexible context, but a miss must not be displayed as a fabricated diff.
// The tips count the lines through the last chunk on each side.
func projectStockUpdate(ctx context.Context, before string, chunks []stockPreviewChunk) (after string, beforeTip, afterTip int, err error) {
	if len(chunks) == 0 {
		return before, 0, 0, nil
	}
	lines := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
	if before == "" {
		lines = nil
	}
	result := make([]string, 0, len(lines))
	position := 0
	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return "", 0, 0, err
		}
		if chunk.context != "" {
			found := -1
			for i := position; i < len(lines); i++ {
				if i%256 == 0 {
					if err := ctx.Err(); err != nil {
						return "", 0, 0, err
					}
				}
				if lines[i] == chunk.context {
					found = i + 1
					break
				}
			}
			if found < 0 {
				return "", 0, 0, errors.New("context not found")
			}
			result = append(result, lines[position:found]...)
			position = found
		}
		if len(chunk.old) == 0 {
			insertion := len(lines)
			if insertion > 0 && lines[insertion-1] == "" {
				insertion-- // Codex's LF-normalizing insertion keeps the final blank line.
			}
			if insertion < position {
				return "", 0, 0, errors.New("insertion conflicts with preceding chunk")
			}
			result = append(result, lines[position:insertion]...)
			result = append(result, chunk.new...)
			position = insertion
			continue
		}
		found := -1
		for i := position; i+len(chunk.old) <= len(lines); i++ {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return "", 0, 0, err
				}
			}
			match := true
			for j, old := range chunk.old {
				if lines[i+j] != old {
					match = false
					break
				}
			}
			if match && (!chunk.endOfFile || i+len(chunk.old) == len(lines)) {
				found = i
				break
			}
		}
		if found < 0 {
			return "", 0, 0, errors.New("patch context not found")
		}
		result = append(result, lines[position:found]...)
		result = append(result, chunk.new...)
		position = found + len(chunk.old)
	}
	beforeTip, afterTip = position, len(result)
	result = append(result, lines[position:]...)
	if len(result) == 0 {
		return "", beforeTip, afterTip, nil
	}
	if result[len(result)-1] == "" {
		return strings.Join(result, "\n"), beforeTip, afterTip, nil
	}
	return strings.Join(result, "\n") + "\n", beforeTip, afterTip, nil
}

// liveDiffLinePrefix keeps the first n lines of text.
func liveDiffLinePrefix(text string, n int) string {
	end := 0
	for range n {
		next := strings.IndexByte(text[end:], '\n')
		if next < 0 {
			return text
		}
		end += next + 1
	}
	return text[:end]
}
