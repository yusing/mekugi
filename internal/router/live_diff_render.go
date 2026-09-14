package router

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

// Rendering consumes the engine's validated rows. File and hunk offsets are
// recorded as rows are emitted, never recovered from a subprocess's output.
func renderLiveDiff(ctx context.Context, files []liveDiffFile, workspace string, width, focusFile int, focus liveDiffChunk) (liveDiffRender, error) {
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
	appendLine := func(line string, highlighted bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = ansi.Truncate(liveDiffGutter(highlighted)+line, max(0, width-1), "")
		renderedBytes += len(line) + 1
		if renderedBytes > maxChangeReadBytes {
			return errors.New("live diff rendering exceeds 64 MiB; use hchanges with a narrower range")
		}
		render.lines = append(render.lines, line)
		return nil
	}
	focusHunks, err := focus.review.Hunks()
	if err != nil {
		return liveDiffRender{}, err
	}
	var bestKind byte
	focusLine, bestDistance := 0, int(^uint(0)>>1)
	if len(focusHunks) > 0 {
		focusLine = focusHunks[len(focusHunks)-1].ChangedStart
	}
	for i, file := range files {
		render.starts[i] = len(render.lines)
		if i == focusFile {
			render.focusOffset, render.focusRow = len(render.lines), len(render.lines)
		}
		action := ""
		for _, chunk := range file.chunks {
			added, removed := chunk.review.LineCounts()
			render.counts[i].added += added
			render.counts[i].removed += removed
			if action == "" && chunk.status == "" {
				action = liveDiffAction(chunk.review, workspace)
			}
		}
		label := fmt.Sprintf("%d/%d  %s", i+1, len(files), liveDiffDisplayPath(workspace, file.path))
		if action != "" {
			label += " · " + action
		}
		counts := render.counts[i]
		statsWidth := len(fmt.Sprintf(" +%d -%d", counts.added, counts.removed))
		headings := strings.Split(ansi.Wrap(liveDiffSafe(label, false), max(1, width-3-statsWidth), ""), "\n")
		for j, heading := range headings {
			if j == 0 {
				heading = liveDiffHeader(heading, width-3, counts)
			} else {
				heading = "\x1b[1m" + heading + "\x1b[22m"
			}
			if err := appendLine(heading, file.highlighted); err != nil {
				return liveDiffRender{}, err
			}
		}
		if len(file.chunks) == 0 {
			if err := appendLine("No unreviewed changes", file.highlighted); err != nil {
				return liveDiffRender{}, err
			}
		}
		// Keep coordinates aligned across a file without reserving four digits
		// (or an entirely absent side) on every source row.
		oldDigits, newDigits := 0, 0
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
						oldDigits = max(oldDigits, len(strconv.Itoa(oldEnd)))
					}
					if row.Kind != '-' {
						newEnd++
						newDigits = max(newDigits, len(strconv.Itoa(newEnd)))
					}
				}
			}
		}
		for j, chunk := range file.chunks {
			review := chunk.review
			hunks := fileHunks[j]
			chunkStart := len(render.lines)
			if chunk.status != "" {
				label := chunk.status
				if action := liveDiffAction(review, workspace); action != "" {
					label += " · " + action
				}
				for line := range strings.SplitSeq(ansi.Wrap(liveDiffSafe(label, false), max(1, width-3), ""), "\n") {
					if err := appendLine(line, chunk.highlighted); err != nil {
						return liveDiffRender{}, err
					}
				}
			}
			for hunkIndex, hunk := range hunks {
				hunkStart := len(render.lines)
				if hunkIndex == 0 {
					hunkStart = chunkStart // Keep prepared status visible when following.
				}
				if i == focusFile && focus.key != "" && chunk.key == focus.key {
					render.focusOffset, render.focusRow, bestDistance = hunkStart, hunkStart, -1
				}
				before, after, err := liveDiffSyntax(ctx, review, hunk.Rows)
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
					if i == focusFile && bestDistance >= 0 && (distance < bestDistance ||
						distance == bestDistance && row.Kind == '+' && bestKind != '+') {
						render.focusRow = len(render.lines)
						render.focusOffset = max(hunkStart, render.focusRow-3)
						bestDistance, bestKind = distance, row.Kind
					}
					left, right, text := "", "", ""
					if row.Kind != '+' {
						left, text = strconv.Itoa(oldLine), before[oldIndex]
						oldLine++
						oldIndex++
					}
					if row.Kind != '-' {
						right, text = strconv.Itoa(newLine), after[newIndex]
						newLine++
						newIndex++
					}
					coordinates := ""
					if oldDigits > 0 {
						coordinates = fmt.Sprintf("%*s", oldDigits, left)
					}
					if newDigits > 0 {
						if oldDigits > 0 {
							coordinates += " "
						}
						coordinates += fmt.Sprintf("%*s", newDigits, right)
					}
					numbers := "\x1b[2m" + coordinates + "│\x1b[22m"
					if width-3 < len(coordinates)+4 {
						numbers = "" // Leave room for source in very narrow panes.
					}
					style := ""
					if row.Kind == '+' {
						style = "\x1b[32m"
					} else if row.Kind == '-' {
						style = "\x1b[31m"
					}
					line := numbers + style + string(row.Kind) + "\x1b[39m" + text + "\x1b[0m"
					if err := appendLine(line, chunk.highlighted); err != nil {
						return liveDiffRender{}, err
					}
					if !strings.HasSuffix(row.Text, "\n") {
						if err := appendLine("\x1b[2m\\ No newline at end of file\x1b[22m", chunk.highlighted); err != nil {
							return liveDiffRender{}, err
						}
					}
				}
			}
		}
	}
	return render, nil
}

// Tokenise the two sides separately so deleted text cannot change the syntax
// state of additions. Never read the workspace to fill uncaptured source gaps.
func liveDiffSyntax(ctx context.Context, review mekugi.ReviewFile, rows []mekugi.ReviewRow) ([]string, []string, error) {
	var before, after liveDiffOutput
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		text := liveDiffSafe(strings.TrimSuffix(row.Text, "\n"), false) + "\n"
		if row.Kind != '+' {
			if _, err := fmt.Fprint(&before, text); err != nil {
				return nil, nil, err
			}
		}
		if row.Kind != '-' {
			if _, err := fmt.Fprint(&after, text); err != nil {
				return nil, nil, err
			}
		}
	}
	old, err := liveDiffColorSource(ctx, review.BeforePath, before.String())
	if err != nil {
		return nil, nil, err
	}
	next, err := liveDiffColorSource(ctx, review.AfterPath, after.String())
	return old, next, err
}

// Syntax is best-effort decoration. Bound lexer input independently of the
// display limit; huge hunks and unknown languages still display exact safe text.
const maxLiveDiffSyntaxBytes = 256 << 10

func liveDiffColorSource(ctx context.Context, path, source string) (lines []string, err error) {
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
	lexer := lexers.Match(path)
	if lexer == nil || len(source) > maxLiveDiffSyntaxBytes {
		return plain, nil
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return plain, nil
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
		remaining = remaining[len(token.Value):]
		style := ""
		switch {
		case token.Type.InCategory(chroma.Keyword):
			style = "\x1b[36m"
		case token.Type.InSubCategory(chroma.LiteralString):
			style = "\x1b[32m"
		case token.Type.InSubCategory(chroma.LiteralNumber):
			style = "\x1b[35m"
		case token.Type.InCategory(chroma.Comment):
			style = "\x1b[90m"
		}
		// Reset each token fragment so scrolling never inherits another row's
		// style. Only foreground colors are generated, never backgrounds.
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				if _, err := fmt.Fprint(&output, "\n"); err != nil {
					return nil, err
				}
			}
			if part != "" {
				if style != "" {
					part = style + part + "\x1b[39m"
				}
				if _, err := fmt.Fprint(&output, part); err != nil {
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
