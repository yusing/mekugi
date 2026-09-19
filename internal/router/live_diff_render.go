package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

// Syntax is independent of wrapping, coordinates, and recency marks. Keep
// bounded caches in each viewer, never shared across sessions.
type liveDiffRenderer struct {
	syntax      map[liveDiffSyntaxKey][]string
	lexers      map[string]chroma.Lexer
	syntaxBytes int
}

type liveDiffSyntaxKey struct {
	theme        liveDiffTheme
	path, source string
}

const maxLiveDiffSyntaxCacheBytes = 8 << 20
const maxLiveDiffSyntaxCacheEntries = 128

func (r *liveDiffRenderer) colorSource(ctx context.Context, theme liveDiffTheme, path, source string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := liveDiffSyntaxKey{theme, path, source}
	if lines, ok := r.syntax[key]; ok {
		return lines, nil
	}
	var lexer chroma.Lexer
	matched := false
	lines, err := liveDiffColorSourceWithMatcher(ctx, theme, path, source, func(path string) chroma.Lexer {
		var cached bool
		lexer, cached = r.lexers[path]
		if !cached {
			lexer = lexers.Match(path)
		}
		matched = true
		return lexer
	})
	if err != nil || source == "" {
		return lines, err
	}
	// Count string headers as well as content: many blank lines still retain
	// a sizeable slice even though the strings themselves have no bytes.
	size := len(path) + len(source) + len(lines)*2*(strconv.IntSize/8)
	if matched {
		// Conservatively charge each source for its selected path and lexer
		// headers, even when several sources share the same selection.
		size += len(path) + 4*(strconv.IntSize/8)
	}
	for _, line := range lines {
		size += len(line)
	}
	if size > maxLiveDiffSyntaxCacheBytes {
		return lines, nil
	}
	if r.syntaxBytes+size > maxLiveDiffSyntaxCacheBytes || len(r.syntax) >= maxLiveDiffSyntaxCacheEntries {
		clear(r.syntax)
		clear(r.lexers)
		r.syntaxBytes = 0
	}
	if r.syntax == nil {
		r.syntax = make(map[liveDiffSyntaxKey][]string)
	}
	r.syntax[key] = lines
	r.syntaxBytes += size
	if matched {
		if r.lexers == nil {
			r.lexers = make(map[string]chroma.Lexer)
		}
		// Include nil results. Selections live only as long as admitted
		// syntax entries, so unknown paths cannot grow a separate cache.
		r.lexers[path] = lexer
	}
	return lines, nil
}

// Rendering consumes the engine's validated rows. File and hunk offsets are
// recorded as rows are emitted, never recovered from a subprocess's output.
func (r *liveDiffRenderer) render(ctx context.Context, theme liveDiffTheme, files []liveDiffFile, workspace string, width, focusFile int, focus liveDiffChunk) (liveDiffRender, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return liveDiffRender{}, err
	}
	sourceBytes := len(focus.review.Diff)
	for _, file := range files {
		sourceBytes += len(file.path)
		for _, chunk := range file.chunks {
			sourceBytes += len(chunk.review.Diff) + len(chunk.status) + len(chunk.review.BeforePath) + len(chunk.review.AfterPath)
		}
	}
	if sourceBytes > maxChangeReadBytes {
		return liveDiffRender{}, errors.New("live diff source exceeds 64 MiB; use hchanges with a narrower range")
	}
	render := liveDiffRender{starts: make([]int, len(files)), counts: make([]liveDiffCounts, len(files))}
	renderedBytes := 0
	appendLine := func(line string, highlighted, continuation bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = ansi.Truncate(liveDiffGutter(highlighted, theme)+line, max(0, width-1), "")
		renderedBytes += len(line) + 1
		if renderedBytes > maxChangeReadBytes {
			return errors.New("live diff rendering exceeds 64 MiB; use hchanges with a narrower range")
		}
		if !continuation {
			render.rowStarts = append(render.rowStarts, len(render.lines))
		}
		render.lines = append(render.lines, line)
		return nil
	}
	focusHunks, err := focus.review.Hunks()
	if err != nil {
		return liveDiffRender{}, err
	}
	var bestKind, focusKind byte
	focusLine, bestDistance := 0, int(^uint(0)>>1)
	// Follow the final changed row, not the first row of a large replacement
	// or creation. Context after the change must not move the anchor.
	for _, hunk := range focusHunks {
		line := hunk.AfterStart
		for _, row := range hunk.Rows {
			if row.Kind != ' ' {
				focusLine, focusKind = line, row.Kind
			}
			if row.Kind != '-' {
				line++
			}
		}
	}
	fileCount := 0
	for _, file := range files {
		if len(file.chunks) > 0 {
			fileCount++
		}
	}
	fileNumber := 0
	for i, file := range files {
		render.starts[i] = len(render.lines)
		if i == focusFile {
			render.focusOffset, render.focusRow = len(render.lines), len(render.lines)
		}
		if len(file.chunks) == 0 {
			continue
		}
		fileNumber++
		action := ""
		incomplete := false
		for _, chunk := range file.chunks {
			incomplete = incomplete || chunk.review.Incomplete != ""
			added, removed := chunk.review.LineCounts()
			render.counts[i].added += added
			render.counts[i].removed += removed
			if action == "" && chunk.status == "" {
				action = liveDiffAction(chunk.review, workspace)
			}
		}
		if incomplete {
			render.counts[i] = liveDiffCounts{-1, -1}
		}
		label := fmt.Sprintf("%d/%d  %s", fileNumber, fileCount, liveDiffDisplayPath(workspace, file.path))
		if action != "" {
			label += " · " + action
		}
		counts := render.counts[i]
		statsWidth := len(fmt.Sprintf(" +%d -%d", counts.added, counts.removed))
		if incomplete {
			statsWidth = len(" counts unavailable")
		}
		headings := strings.Split(ansi.Wrap(liveDiffSafe(label, false), max(1, width-3-statsWidth), ""), "\n")
		for j, heading := range headings {
			if j == 0 {
				heading = liveDiffHeader(heading, width-3, counts, theme)
			} else {
				heading = "\x1b[1m" + heading + "\x1b[22m"
			}
			if err := appendLine(heading, file.highlighted, j > 0); err != nil {
				return liveDiffRender{}, err
			}
		}
		// Keep one coordinate column aligned across the file. Deletions use
		// old line numbers; additions and context use new line numbers.
		digits := 0
		fileHunks := make([][]mekugi.ReviewHunk, len(file.chunks))
		for j, chunk := range file.chunks {
			hunks, err := chunk.review.Hunks()
			if err != nil {
				return liveDiffRender{}, err
			}
			fileHunks[j] = hunks
			for _, hunk := range hunks {
				oldEnd, newEnd := hunk.BeforeStart, hunk.AfterStart
				for _, row := range hunk.Rows {
					if row.Kind != '+' {
						oldEnd++
						digits = max(digits, len(strconv.Itoa(oldEnd)))
					}
					if row.Kind != '-' {
						newEnd++
						digits = max(digits, len(strconv.Itoa(newEnd)))
					}
				}
			}
		}
		numberWidth := digits + 1
		if width-3 < digits+4 {
			numberWidth = 0 // Leave room for source in very narrow panes.
		}
		sourceWidth := max(1, width-4-numberWidth)
		continuationNumbers := ""
		if numberWidth > 0 {
			continuationNumbers = "\x1b[2m" + strings.Repeat(" ", digits) + "│\x1b[22m"
		}
		preferFocusKey := focus.key != "" && slices.ContainsFunc(file.chunks, func(chunk liveDiffChunk) bool { return chunk.key == focus.key })
		preferHighlighted := i == focusFile && focus.highlighted
		for j, chunk := range file.chunks {
			review := chunk.review
			hunks := fileHunks[j]
			chunkStart := len(render.lines)
			if chunk.status != "" {
				label := chunk.status
				if action := liveDiffAction(review, workspace); action != "" {
					label += " · " + action
				}
				continuation := false
				for line := range strings.SplitSeq(ansi.Wrap(liveDiffSafe(label, false), max(1, width-3), ""), "\n") {
					if err := appendLine(line, chunk.highlighted, continuation); err != nil {
						return liveDiffRender{}, err
					}
					continuation = true
				}
			}
			// Keep deletion metadata and counts, but omit the removed file's source.
			if review.BeforePath != "" && review.AfterPath == "" {
				continue
			}
			for hunkIndex, hunk := range hunks {
				hunkStart := len(render.lines)
				if hunkIndex == 0 {
					hunkStart = chunkStart // Keep prepared status visible when following.
				}
				before, after, err := r.colorHunk(ctx, theme, review, hunk.Rows)
				if err != nil {
					return liveDiffRender{}, err
				}
				oldLine, newLine := hunk.BeforeStart+1, hunk.AfterStart+1
				oldIndex, newIndex := 0, 0
				for _, row := range hunk.Rows {
					// A composed hunk can span the entire new file or several
					// adjacent updates. Follow the actual changed coordinate,
					// not the start of that potentially very large hunk.
					distance := max(newLine-1-focusLine, focusLine-(newLine-1))
					isFocus := i == focusFile &&
						(!preferFocusKey || chunk.key == focus.key) &&
						(preferFocusKey || !preferHighlighted || chunk.highlighted) &&
						(distance < bestDistance ||
							distance == bestDistance && row.Kind == focusKind && (bestKind != focusKind || focusKind == '-'))
					if isFocus {
						bestDistance, bestKind = distance, row.Kind
					}
					number, text := "", ""
					if row.Kind != '+' {
						number, text = strconv.Itoa(oldLine), before[oldIndex]
						oldLine++
						oldIndex++
					}
					if row.Kind != '-' {
						number, text = strconv.Itoa(newLine), after[newIndex]
						newLine++
						newIndex++
					}
					numbers := ""
					if numberWidth > 0 {
						numbers = "\x1b[2m" + strings.Repeat(" ", max(0, digits-len(number))) + number + "│\x1b[22m"
					}
					// Wrap source independently of the fixed coordinate column.
					fragments := ansi.Hardwrap(text, sourceWidth, true)
					carry := ""
					continuation := false
					for fragment := range strings.SplitSeq(fragments, "\n") {
						fragment = carry + fragment
						// Syntax emits only foreground SGR. Restore its last color
						// on continuations, which may be the first visible row.
						if start := strings.LastIndex(fragment, "\x1b["); start >= 0 {
							if end := strings.IndexByte(fragment[start:], 'm'); end >= 0 {
								carry = fragment[start : start+end+1]
							}
						}
						prefix := numbers
						if continuation && numbers != "" {
							prefix = continuationNumbers
						}
						line := liveDiffSourceLine(theme, width, prefix, fragment, row.Kind)
						if err := appendLine(line, chunk.highlighted, continuation); err != nil {
							return liveDiffRender{}, err
						}
						continuation = true
					}
					if isFocus {
						render.focusRow = len(render.lines) - 1
						render.focusOffset = max(hunkStart, render.focusRow-3)
					}
					if !strings.HasSuffix(row.Text, "\n") {
						if err := appendLine("\x1b[2m\\ No newline at end of file\x1b[22m", chunk.highlighted, false); err != nil {
							return liveDiffRender{}, err
						}
					}
				}
			}
		}
	}
	return render, nil
}

func liveDiffSourceLine(theme liveDiffTheme, width int, numbers, fragment string, kind byte) string {
	style := ""
	if kind == '+' {
		style = theme.foreground(chroma.GenericInserted)
	} else if kind == '-' {
		style = theme.foreground(chroma.GenericDeleted)
	}
	line := numbers + style + string(kind) + "\x1b[39m" + fragment
	if background := theme.rowBackground(kind); background != "" {
		// Token resets restore a readable foreground on the fill.
		base := theme.foreground(chroma.NameOther)
		line = strings.ReplaceAll(line, "\x1b[39m", base)
		line = ansi.Truncate(line, max(0, width-3), "")
		line += strings.Repeat(" ", max(0, width-3-ansi.StringWidth(line)))
		line = background + base + line
	}
	return line + "\x1b[0m"
}

// Tokenise the two sides separately so deleted text cannot change the syntax
// state of additions. Never read the workspace to fill uncaptured source gaps.
func (r *liveDiffRenderer) colorHunk(ctx context.Context, theme liveDiffTheme, review mekugi.ReviewFile, rows []mekugi.ReviewRow) ([]string, []string, error) {
	var before, after liveDiffOutput
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		text := liveDiffSafe(strings.TrimSuffix(row.Text, "\n"), false) + "\n"
		if row.Kind != '+' {
			if _, err := before.WriteString(text); err != nil {
				return nil, nil, err
			}
		}
		if row.Kind != '-' {
			if _, err := after.WriteString(text); err != nil {
				return nil, nil, err
			}
		}
	}
	old, err := r.colorSource(ctx, theme, review.BeforePath, before.String())
	if err != nil {
		return nil, nil, err
	}
	next, err := r.colorSource(ctx, theme, review.AfterPath, after.String())
	return old, next, err
}

// Syntax is best-effort decoration. Bound lexer input independently of the
// display limit; huge hunks and unknown languages still display exact safe text.
const maxLiveDiffSyntaxBytes = 256 << 10

func liveDiffColorSource(ctx context.Context, theme liveDiffTheme, path, source string) ([]string, error) {
	return liveDiffColorSourceWithMatcher(ctx, theme, path, source, lexers.Match)
}

func liveDiffColorSourceWithMatcher(ctx context.Context, theme liveDiffTheme, path, source string, match func(string) chroma.Lexer) (lines []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == "" {
		return nil, nil
	}
	plain := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	// Third-party lexers can panic while iterating incomplete source. Syntax
	// decoration must not take down the viewer or expose half-colored content.
	defer func() {
		if recover() != nil {
			lines, err = plain, ctx.Err()
		}
	}()
	if len(source) > maxLiveDiffSyntaxBytes {
		return plain, nil
	}
	lexer := match(path)
	if lexer == nil {
		return plain, nil
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return plain, nil
	}
	var commands map[int]string
	if lexer.Config().Name == "Bash" {
		commands = liveDiffShellCommands(source)
	}
	var output liveDiffOutput
	remaining := source
	for token := iterator(); token != chroma.EOF; token = iterator() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(remaining, token.Value) {
			return plain, nil
		}
		if token.Type.InCategory(chroma.Text) && token.Value != "" && commands[len(source)-len(remaining)] == token.Value {
			token.Type = chroma.NameFunction
		}
		remaining = remaining[len(token.Value):]
		style := theme.foreground(token.Type)
		// Reset each token fragment so scrolling never inherits another row's
		// style. Only foreground colors are generated, never backgrounds.
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				if _, err := output.WriteString("\n"); err != nil {
					return nil, err
				}
			}
			if part != "" {
				if style != "" {
					part = style + part + "\x1b[39m"
				}
				if _, err := output.WriteString(part); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if remaining != "" {
		return plain, nil
	}
	return strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n"), nil
}
