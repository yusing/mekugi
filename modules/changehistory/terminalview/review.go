package terminalview

// Source: internal/livediff/render.go@[ebde9ad5]
import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
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
func (r *Renderer) originLabel(origin presentation.Origin) string {
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
func (r *Renderer) provenance(file presentation.File) string {
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
		part := textsanitize.Safe(origin.Change, false)
		if name != "" {
			part += " " + color + textsanitize.Safe(name, false) + "\x1b[0m"
		}
		if origin.Source != "" {
			part += diffrender.Subtle + "·" + textsanitize.Safe(origin.Source, false) + diffrender.SubtleReset
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
func (r *Renderer) Render(ctx context.Context, theme diffrender.Theme, files []presentation.File, workspace string, width int) (Render, error) {
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
	var source diffrender.LineSource
	appendLine := func(line string, highlighted, continuation bool) error {
		return render.Layout.AppendLine(ctx, theme, width, line, highlighted, continuation, source)
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
		source = diffrender.LineSource{Path: file.Path, Content: diffrender.GutterWidth}
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
		headings := strings.Split(ansi.Wrap(textsanitize.Safe(label, false), max(1, width-3-statsWidth), ""), "\n")
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
		for j, chunk := range file.Chunks {
			review := chunk.Review
			hunks := fileHunks[j]
			source = diffrender.LineSource{Change: cmp.Or(chunk.Change, originChanges(file.Origins)), Path: file.Path, Content: diffrender.GutterWidth}
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
					if origin := r.originLabel(presentation.Origin{Caller: chunk.Caller, Source: chunk.Source}); origin != "" && chunk.Change != "" {
						label += " · " + origin
					}
					if action := fileAction(review, workspace); action != "" {
						label += " · " + action
					}
				}
				continuation := false
				for line := range strings.SplitSeq(ansi.Wrap(textsanitize.Safe(label, false), max(1, width-3), ""), "\n") {
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
				if err := r.syntax.AppendHunk(ctx, &render.Layout, theme, review, hunk, width, digits, chunk.Highlighted, r.LayoutOnly, source); err != nil {
					return Render{}, err
				}
			}
		}
	}
	return render, nil
}

// originChanges names the changes a composed file shows, comma-separated;
// more than three keep the first three and a count.
func originChanges(origins []presentation.Origin) string {
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

func fileAction(file diffmodel.ReviewFile, workspace string) string {
	switch {
	case file.BeforePath == "" && file.AfterPath != "":
		return "New file"
	case file.AfterPath == "" && file.BeforePath != "":
		return "Deleted file"
	case file.BeforePath != file.AfterPath:
		return pathdisplay.ForWorkspace(workspace, file.BeforePath) + " → " + pathdisplay.ForWorkspace(workspace, file.AfterPath)
	default:
		return ""
	}
}

// Render combines shared terminal rows with feature-owned file sections and counts.
type Render struct {
	diffrender.Layout
	Starts []int
	Counts []diffmodel.Counts
}
