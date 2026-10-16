package livediff

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Renderer paints captured-change provenance and geometry using viewer-local
// shared syntax decoration.
type Renderer struct {
	// LayoutOnly defers complete-hunk syntax decoration until PaintViewport.
	LayoutOnly bool
	syntax     diffrender.Renderer
	// Caller presents a canonical agent path as a display name and an SGR
	// color prefix. Without it, paths appear as recorded, uncolored.
	Caller func(path string) (name, color string)
}

func (r *Renderer) caller(path string) (string, string) {
	if r.Caller == nil {
		return path, ""
	}
	return r.Caller(path)
}

// originLabel is the plain attribution of a capture: ID · caller · source.
func (r *Renderer) originLabel(origin Origin) string {
	caller := origin.Caller
	if caller != "" {
		caller, _ = r.caller(caller)
	}
	var parts []string
	for _, part := range []string{origin.Change, caller, origin.Source} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " · ")
}

// provenance lists the changes a composed file combines, colored by caller.
func (r *Renderer) provenance(file File) string {
	if len(file.Origins) == 0 && file.Baseline == 0 {
		return ""
	}
	var parts []string
	for i, origin := range file.Origins {
		if i == 4 {
			parts = append(parts, fmt.Sprintf(diffrender.Subtle+"+%d more"+diffrender.SubtleReset, len(file.Origins)-i))
			break
		}
		name, color := r.caller(origin.Caller)
		part := diffrender.Safe(origin.Change, false)
		if name != "" {
			part += " " + color + diffrender.Safe(name, false) + "\x1b[0m"
		}
		if origin.Source != "" {
			part += diffrender.Subtle + "·" + diffrender.Safe(origin.Source, false) + diffrender.SubtleReset
		}
		parts = append(parts, part)
	}
	if file.Baseline > 0 {
		noun := "change"
		if file.Baseline > 1 {
			noun += "s"
		}
		attribution := "by other callers"
		parts = append(parts, fmt.Sprintf(diffrender.Subtle+"%d %s %s as baseline"+diffrender.SubtleReset, file.Baseline, noun, attribution))
	}
	return diffrender.Subtle + "┄" + diffrender.SubtleReset + " " + strings.Join(parts, "  ")
}

// Rendering consumes the engine's validated rows. File and hunk offsets are
// recorded as rows are emitted, never recovered from a subprocess's output.
func (r *Renderer) Render(ctx context.Context, theme diffrender.Theme, files []File, workspace string, width int) (Render, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Render{}, err
	}
	sourceBytes := 0
	for _, file := range files {
		sourceBytes += len(file.Path)
		for _, chunk := range file.Chunks {
			sourceBytes += len(chunk.Review.Diff) + len(chunk.Status) + len(chunk.Review.BeforePath) + len(chunk.Review.AfterPath)
		}
	}
	if sourceBytes > diffrender.MaxSourceBytes {
		return Render{}, errors.New("live diff source exceeds 64 MiB; use mchanges with a narrower range")
	}
	render := Render{Starts: make([]int, len(files)), Counts: make([]diffmodel.Counts, len(files))}
	renderedBytes := 0
	var source LineSource // Attribution of the rows being appended.
	appendLine := func(line string, highlighted, continuation bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = ansi.Truncate(diffrender.Gutter(highlighted, theme)+line, max(0, width-1), "")
		renderedBytes += len(line) + 1
		if renderedBytes > diffrender.MaxSourceBytes {
			return errors.New("live diff rendering exceeds 64 MiB; use mchanges with a narrower range")
		}
		if !continuation {
			render.RowStarts = append(render.RowStarts, len(render.Lines))
		}
		render.Lines = append(render.Lines, line)
		render.Sources = append(render.Sources, source)
		return nil
	}
	fileCount := 0
	for _, file := range files {
		if len(file.Chunks) > 0 {
			fileCount++
		}
	}
	fileNumber := 0
	for i, file := range files {
		render.Starts[i] = len(render.Lines)
		if len(file.Chunks) == 0 {
			continue
		}
		source = LineSource{Path: file.Path, Content: gutterWidth}
		fileNumber++
		action := ""
		pathLabel := pathdisplay.ForWorkspace(workspace, file.Path)
		incomplete := false
		for _, chunk := range file.Chunks {
			incomplete = incomplete || chunk.Review.Incomplete != ""
			added, removed := chunk.Review.LineCounts()
			render.Counts[i].Added += added
			render.Counts[i].Removed += removed
			if action == "" && chunk.Status == "" {
				action = fileAction(chunk.Review, workspace)
				if chunk.Review.BeforePath != "" && chunk.Review.AfterPath != "" && chunk.Review.BeforePath != chunk.Review.AfterPath {
					pathLabel, action = action, ""
				}
			}
		}
		if incomplete {
			render.Counts[i] = diffmodel.Counts{-1, -1}
		}
		label := fmt.Sprintf("%d/%d  %s", fileNumber, fileCount, pathLabel)
		if action != "" {
			label += " · " + action
		}
		counts := render.Counts[i]
		statsWidth := len(fmt.Sprintf(" +%d -%d", counts.Added, counts.Removed))
		if incomplete {
			statsWidth = len(" counts unavailable")
		}
		headings := strings.Split(ansi.Wrap(diffrender.Safe(label, false), max(1, width-3-statsWidth), ""), "\n")
		for j, heading := range headings {
			if j == 0 {
				heading = diffrender.Header(heading, width-3, counts, theme)
			} else {
				heading = "\x1b[1m" + heading + "\x1b[22m"
			}
			if err := appendLine(heading, file.Highlighted, j > 0); err != nil {
				return Render{}, err
			}
		}
		if line := r.provenance(file); line != "" {
			if err := appendLine(line, file.Highlighted, false); err != nil {
				return Render{}, err
			}
		}
		// Keep one coordinate column aligned across the file. Deletions use
		// old line numbers; additions and context use new line numbers.
		digits := 0
		fileHunks := make([][]diffmodel.ReviewHunk, len(file.Chunks))
		for j, chunk := range file.Chunks {
			hunks, err := chunk.Review.Hunks()
			if err != nil {
				return Render{}, err
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
			continuationNumbers = diffrender.Subtle + strings.Repeat(" ", digits) + "│" + diffrender.SubtleReset
		}
		for j, chunk := range file.Chunks {
			review := chunk.Review
			hunks := fileHunks[j]
			source = LineSource{Change: cmp.Or(chunk.Change, originChanges(file.Origins)), Path: file.Path, Content: gutterWidth}
			if chunk.Status != "" {
				label := chunk.Status
				if review.Directory {
					status := "M"
					if review.BeforePath == "" {
						status = "A"
					}
					if review.AfterPath == "" {
						status = "D"
					}
					label = status + " " + pathdisplay.ForWorkspace(workspace, cmp.Or(review.AfterPath, review.BeforePath)) + "/"
				} else {
					if origin := r.originLabel(Origin{Caller: chunk.Caller, Source: chunk.Source}); origin != "" && chunk.Change != "" {
						label += " · " + origin
					}
					if action := fileAction(review, workspace); action != "" {
						label += " · " + action
					}
				}
				continuation := false
				for line := range strings.SplitSeq(ansi.Wrap(diffrender.Safe(label, false), max(1, width-3), ""), "\n") {
					if err := appendLine(line, chunk.Highlighted, continuation); err != nil {
						return Render{}, err
					}
					continuation = true
				}
			}
			// Keep deletion metadata and counts, but omit the removed file's source.
			if review.BeforePath != "" && review.AfterPath == "" {
				continue
			}
			for _, hunk := range hunks {
				render.Hunks = append(render.Hunks, len(render.Lines))
				var before, after []string
				if !r.LayoutOnly {
					var err error
					before, after, err = r.syntax.ColorHunk(ctx, theme, review, hunk.Rows)
					if err != nil {
						return Render{}, err
					}
				}
				var paint syntaxHunk
				if r.LayoutOnly {
					paint = syntaxHunk{renderer: &r.syntax, theme: theme, review: review, rows: hunk.Rows,
						start: len(render.Lines), width: width, sourceWidth: sourceWidth, digits: digits,
						numberWidth: numberWidth, highlighted: chunk.Highlighted}
				}
				oldLine, newLine := hunk.BeforeStart+1, hunk.AfterStart+1
				oldIndex, newIndex := 0, 0
				for _, row := range hunk.Rows {
					coordinate, text := 0, ""
					if r.LayoutOnly {
						text = diffrender.Safe(strings.TrimSuffix(row.Text, "\n"), false)
						paint.rowStarts = append(paint.rowStarts, len(render.Lines))
					}
					if row.Kind != '+' {
						coordinate = oldLine
						if !r.LayoutOnly {
							text = before[oldIndex]
						}
						oldLine++
						oldIndex++
					}
					if row.Kind != '-' {
						coordinate = newLine
						if !r.LayoutOnly {
							text = after[newIndex]
						}
						newLine++
						newIndex++
					}
					number := strconv.Itoa(coordinate)
					source.Line, source.Deleted = coordinate, row.Kind == '-'
					numbers := ""
					if numberWidth > 0 {
						numbers = diffrender.Subtle + strings.Repeat(" ", max(0, digits-len(number))) + number + "│" + diffrender.SubtleReset
					}
					for n, line := range sourceFragments(theme, width, sourceWidth, numbers, continuationNumbers, text, row.Kind) {
						prefix := numbers
						if n > 0 && numbers != "" {
							prefix = continuationNumbers
						}
						source.Content = gutterWidth + ansi.StringWidth(prefix)
						if err := appendLine(line, chunk.Highlighted, n > 0); err != nil {
							return Render{}, err
						}
					}
					source.Line, source.Deleted, source.Content = 0, false, gutterWidth
					if !strings.HasSuffix(row.Text, "\n") {
						if err := appendLine(diffrender.Subtle+"\\ No newline at end of file"+diffrender.SubtleReset, chunk.Highlighted, false); err != nil {
							return Render{}, err
						}
					}
				}
				if r.LayoutOnly {
					paint.end = len(render.Lines)
					render.syntaxHunks = append(render.syntaxHunks, paint)
				}
			}
		}
	}
	render.renderedBytes = renderedBytes
	return render, nil
}

// Source wrapping is shared by layout and decoration, so syntax cannot change geometry.
func sourceFragments(theme diffrender.Theme, width, sourceWidth int, numbers, continuationNumbers, text string, kind byte) []string {
	var lines []string
	for _, fragment := range diffrender.WrapSource(text, sourceWidth) {
		prefix := numbers
		if len(lines) > 0 && numbers != "" {
			prefix = continuationNumbers
		}
		lines = append(lines, diffrender.SourceLine(theme, width, prefix, fragment, kind))
	}
	return lines
}

type syntaxHunk struct {
	renderer                                            *diffrender.Renderer
	theme                                               diffrender.Theme
	review                                              diffmodel.ReviewFile
	rows                                                []diffmodel.ReviewRow
	rowStarts                                           []int
	start, end, width, sourceWidth, digits, numberWidth int
	highlighted, painted                                bool
}

// PaintViewport decorates intersecting complete hunks. Coordinates and source
// attribution were fixed during layout; off-screen hunks never invoke a lexer.
func (render *Render) PaintViewport(ctx context.Context, start, end int) error {
	if end <= start {
		return nil
	}
	for i := range render.syntaxHunks {
		h := &render.syntaxHunks[i]
		if h.painted || h.end <= start || h.start >= end {
			continue
		}
		before, after, err := h.renderer.ColorHunk(ctx, h.theme, h.review, h.rows)
		if err != nil {
			return err
		}
		var lines []string
		oldIndex, newIndex := 0, 0
		for n, row := range h.rows {
			text := ""
			if row.Kind != '+' {
				text = before[oldIndex]
				oldIndex++
			}
			if row.Kind != '-' {
				text = after[newIndex]
				newIndex++
			}
			numbers, continuation := "", ""
			if h.numberWidth > 0 {
				number := strconv.Itoa(render.Sources[h.rowStarts[n]].Line)
				numbers = diffrender.Subtle + strings.Repeat(" ", max(0, h.digits-len(number))) + number + "│" + diffrender.SubtleReset
				continuation = diffrender.Subtle + strings.Repeat(" ", h.digits) + "│" + diffrender.SubtleReset
			}
			lines = append(lines, sourceFragments(h.theme, h.width, h.sourceWidth, numbers, continuation, text, row.Kind)...)
			if !strings.HasSuffix(row.Text, "\n") {
				lines = append(lines, diffrender.Subtle+"\\ No newline at end of file"+diffrender.SubtleReset)
			}
		}
		if len(lines) != h.end-h.start {
			return errors.New("live diff syntax changed layout")
		}
		delta := 0
		for n, line := range lines {
			lines[n] = ansi.Truncate(diffrender.Gutter(h.highlighted, h.theme)+line, max(0, h.width-1), "")
			delta += len(lines[n]) - len(render.Lines[h.start+n])
		}
		if render.renderedBytes+delta > diffrender.MaxSourceBytes {
			return errors.New("live diff rendering exceeds 64 MiB; use mchanges with a narrower range")
		}
		copy(render.Lines[h.start:h.end], lines)
		render.renderedBytes += delta
		h.painted = true
	}
	return nil
}

// originChanges names the changes a composed file shows, comma-separated;
// more than three keep the first three and a count.
func originChanges(origins []Origin) string {
	var changes []string
	for _, origin := range origins {
		if origin.Change != "" {
			changes = append(changes, origin.Change)
		}
	}
	if len(changes) > 3 {
		return strings.Join(changes[:3], ",") + fmt.Sprintf(" +%d", len(changes)-3)
	}
	return strings.Join(changes, ",")
}
