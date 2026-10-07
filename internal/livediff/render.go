package livediff

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// Syntax is independent of wrapping, coordinates, and recency marks. Keep
// bounded caches in each viewer, never shared across sessions.
type Renderer struct {
	// LayoutOnly defers complete-hunk syntax decoration until PaintViewport.
	LayoutOnly  bool
	syntax      map[syntaxKey][]string
	lexers      map[string]chroma.Lexer
	syntaxBytes int
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
	var parts []string
	for _, part := range []string{origin.Change, origin.Caller, origin.Source} {
		if part == origin.Caller && part != "" {
			part, _ = r.caller(part)
		}
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
			parts = append(parts, fmt.Sprintf(Subtle+"+%d more"+SubtleReset, len(file.Origins)-i))
			break
		}
		name, color := r.caller(origin.Caller)
		part := Safe(origin.Change, false)
		if name != "" {
			part += " " + color + Safe(name, false) + "\x1b[0m"
		}
		if origin.Source != "" {
			part += Subtle + "·" + Safe(origin.Source, false) + SubtleReset
		}
		parts = append(parts, part)
	}
	if file.Baseline > 0 {
		noun := "change"
		if file.Baseline > 1 {
			noun += "s"
		}
		parts = append(parts, fmt.Sprintf(Subtle+"%d %s by other callers as baseline"+SubtleReset, file.Baseline, noun))
	}
	return Subtle + "┄" + SubtleReset + " " + strings.Join(parts, "  ")
}

type syntaxKey struct {
	theme        Theme
	path, source string
}

const maxSyntaxCacheBytes = 8 << 20
const maxSyntaxCacheEntries = 128

// ColorSource uses the filename when supplied, otherwise content analysis.
func (r *Renderer) ColorSource(ctx context.Context, theme Theme, path, source string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := syntaxKey{theme, path, source}
	if lines, ok := r.syntax[key]; ok {
		return lines, nil
	}
	var lexer chroma.Lexer
	matched := false
	lines, err := colorSourceWithMatcher(ctx, theme, path, source, func(path string) chroma.Lexer {
		if path == "" {
			// A detected lexer belongs to this content, not the empty path.
			// The rendered-source cache below also retains negative detections.
			return lexers.Analyse(source)
		}
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
	if size > maxSyntaxCacheBytes {
		return lines, nil
	}
	if r.syntaxBytes+size > maxSyntaxCacheBytes || len(r.syntax) >= maxSyntaxCacheEntries {
		clear(r.syntax)
		clear(r.lexers)
		r.syntaxBytes = 0
	}
	if r.syntax == nil {
		r.syntax = make(map[syntaxKey][]string)
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
func (r *Renderer) Render(ctx context.Context, theme Theme, files []File, workspace string, width int) (Render, error) {
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
	if sourceBytes > MaxSourceBytes {
		return Render{}, errors.New("live diff source exceeds 64 MiB; use mchanges with a narrower range")
	}
	render := Render{Starts: make([]int, len(files)), Counts: make([]Counts, len(files))}
	renderedBytes := 0
	var source LineSource // Attribution of the rows being appended.
	appendLine := func(line string, highlighted, continuation bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = ansi.Truncate(Gutter(highlighted, theme)+line, max(0, width-1), "")
		renderedBytes += len(line) + 1
		if renderedBytes > MaxSourceBytes {
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
			render.Counts[i] = Counts{-1, -1}
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
		headings := strings.Split(ansi.Wrap(Safe(label, false), max(1, width-3-statsWidth), ""), "\n")
		for j, heading := range headings {
			if j == 0 {
				heading = Header(heading, width-3, counts, theme)
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
		fileHunks := make([][]mekugi.ReviewHunk, len(file.Chunks))
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
			continuationNumbers = Subtle + strings.Repeat(" ", digits) + "│" + SubtleReset
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
				for line := range strings.SplitSeq(ansi.Wrap(Safe(label, false), max(1, width-3), ""), "\n") {
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
					before, after, err = r.ColorHunk(ctx, theme, review, hunk.Rows)
					if err != nil {
						return Render{}, err
					}
				}
				var paint syntaxHunk
				if r.LayoutOnly {
					paint = syntaxHunk{renderer: r, theme: theme, review: review, rows: hunk.Rows,
						start: len(render.Lines), width: width, sourceWidth: sourceWidth, digits: digits,
						numberWidth: numberWidth, highlighted: chunk.Highlighted}
				}
				oldLine, newLine := hunk.BeforeStart+1, hunk.AfterStart+1
				oldIndex, newIndex := 0, 0
				for _, row := range hunk.Rows {
					coordinate, text := 0, ""
					if r.LayoutOnly {
						text = Safe(strings.TrimSuffix(row.Text, "\n"), false)
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
						numbers = Subtle + strings.Repeat(" ", max(0, digits-len(number))) + number + "│" + SubtleReset
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
						if err := appendLine(Subtle+"\\ No newline at end of file"+SubtleReset, chunk.Highlighted, false); err != nil {
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
func sourceFragments(theme Theme, width, sourceWidth int, numbers, continuationNumbers, text string, kind byte) []string {
	var lines []string
	for _, fragment := range WrapSource(text, sourceWidth) {
		prefix := numbers
		if len(lines) > 0 && numbers != "" {
			prefix = continuationNumbers
		}
		lines = append(lines, SourceLine(theme, width, prefix, fragment, kind))
	}
	return lines
}

// WrapSource keeps both syntax foregrounds and word backgrounds on independently
// painted continuation rows. SourceLine keeps gutters and padding at the row fill.
func WrapSource(text string, width int) []string {
	var style uv.Style
	parser := ansi.GetParser()
	defer func() {
		parser.SetHandler(ansi.Handler{})
		ansi.PutParser(parser)
	}()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	var lines []string
	for fragment := range strings.SplitSeq(ansi.Hardwrap(text, max(1, width), true), "\n") {
		carry := ""
		if !style.IsZero() {
			carry = style.String()
		}
		lines = append(lines, carry+fragment)
		parser.Parse([]byte(fragment))
	}
	return lines
}

type syntaxHunk struct {
	renderer                                            *Renderer
	theme                                               Theme
	review                                              mekugi.ReviewFile
	rows                                                []mekugi.ReviewRow
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
				numbers = Subtle + strings.Repeat(" ", max(0, h.digits-len(number))) + number + "│" + SubtleReset
				continuation = Subtle + strings.Repeat(" ", h.digits) + "│" + SubtleReset
			}
			lines = append(lines, sourceFragments(h.theme, h.width, h.sourceWidth, numbers, continuation, text, row.Kind)...)
			if !strings.HasSuffix(row.Text, "\n") {
				lines = append(lines, Subtle+"\\ No newline at end of file"+SubtleReset)
			}
		}
		if len(lines) != h.end-h.start {
			return errors.New("live diff syntax changed layout")
		}
		delta := 0
		for n, line := range lines {
			lines[n] = ansi.Truncate(Gutter(h.highlighted, h.theme)+line, max(0, h.width-1), "")
			delta += len(lines[n]) - len(render.Lines[h.start+n])
		}
		if render.renderedBytes+delta > MaxSourceBytes {
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

func SourceLine(theme Theme, width int, numbers, fragment string, kind byte) string {
	style := ""
	if kind == '+' {
		style = theme.Foreground(chroma.GenericInserted)
	} else if kind == '-' {
		style = theme.Foreground(chroma.GenericDeleted)
	}
	line := numbers + style + string(kind) + "\x1b[39m" + fragment
	if background := theme.RowBackground(kind); background != "" {
		// Token resets restore a readable foreground on the fill.
		base := theme.Foreground(chroma.NameOther)
		line = strings.ReplaceAll(line, "\x1b[39m", base)
		line = ansi.Truncate(line, max(0, width-3), "")
		line += background // Word emphasis must not extend into right padding.
		line += strings.Repeat(" ", max(0, width-3-ansi.StringWidth(line)))
		line = background + base + line
	}
	return line + "\x1b[0m"
}

// Tokenise the two sides separately so deleted text cannot change the syntax
// state of additions. Never read the workspace to fill uncaptured source gaps.
func (r *Renderer) ColorHunk(ctx context.Context, theme Theme, review mekugi.ReviewFile, rows []mekugi.ReviewRow) ([]string, []string, error) {
	var before, after Output
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		text := Safe(strings.TrimSuffix(row.Text, "\n"), false) + "\n"
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
	old, err := r.ColorSource(ctx, theme, review.BeforePath, before.String())
	if err != nil {
		return nil, nil, err
	}
	next, err := r.ColorSource(ctx, theme, review.AfterPath, after.String())
	if err != nil {
		return nil, nil, err
	}
	return colorHunkWords(ctx, theme, rows, old, next)
}

// Syntax is best-effort decoration. Bound lexer input independently of the
// display limit; huge hunks and unknown languages still display exact safe text.
const MaxSyntaxBytes = 256 << 10

func colorSourceWithMatcher(ctx context.Context, theme Theme, path, source string, match func(string) chroma.Lexer) (lines []string, err error) {
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
	if len(source) > MaxSyntaxBytes {
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
		commands = shellCommands(source)
	}
	var output Output
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
		style := theme.Foreground(token.Type)
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
