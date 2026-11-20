package terminalview

// Source: internal/ui/diffview/preview.go@[a42ac9f1]
import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

const (
	PreviewFrameDelay = 33 * time.Millisecond
	// Revealed rows fade in; rows revealed together cascade within a bound.
	// A fade starts partly visible, so a coarsely sampled frame, as over mosh
	// or a slow link, still shows readable text rather than a blank row.
	liveDiffPreviewFade      = 160 * time.Millisecond
	liveDiffPreviewFadeFloor = .4
	liveDiffPreviewStagger   = 16 * time.Millisecond
	PreviewMaxStagger        = 96 * time.Millisecond
	// Keep consecutive target units visually distinct: the next unit starts
	// after the preceding unit's last row has finished fading in.
	PreviewUnitDelay = liveDiffPreviewFade + PreviewMaxStagger
	// A finished call keeps distinct unit reveals only this long before its
	// remaining input is shown at once.
	PreviewFinishDrain = time.Second
	// While following additions, each deletion run shows this many rows
	// nearest the tip; a marker row counts the rest.
	liveDiffPreviewKeptDeletions = 3
)

// Previews have their own viewport and lifecycle. They never change the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type PreviewPane struct {
	Views    map[string]*PreviewView
	Order    []string
	Motion   PreviewMotion
	batches  map[string]*previewBatch
	pending  map[string]pendingPreview
	sequence uint64
}

// Motion is display-only. Without it, rows appear at their final colors.
type PreviewMotion struct {
	Enabled bool
	Canvas  diffrender.Canvas
	Now     time.Time
}

// Each file owns its source window, syntax cache, and completion state.
type PreviewView struct {
	Current   previewmodel.Preview
	Complete  bool
	numbers   diffrender.Numbers // Line-number columns only grow, so the source never shifts sideways.
	Rendered  previewmodel.Preview
	Focus     int
	Paused    bool
	ScrollRow int
	rows      int
	renderer  diffrender.Renderer
	Source    []PreviewRow
	born      []time.Time // When each source row was revealed, for its fade.
	updated   time.Time   // The latest snapshot or first completion arrival.
}

// PreviewRow carries its old- and new-file coordinates; zero means the
// row has none on that side.
type PreviewRow struct {
	Old, New int
	Kind     byte
	Text     string
}

func (p *PreviewPane) Update(preview previewmodel.Preview, now time.Time) {
	view := p.Views[preview.ID]
	if preview.Workspace != "" && preview.Status == "" && preview.Input == "" && len(preview.Files) == 0 {
		delete(p.Views, preview.ID)
		p.Order = slices.DeleteFunc(p.Order, func(id string) bool { return id == preview.ID })
		return
	}
	if preview.Workspace == "" {
		if view != nil && !view.Complete {
			view.Complete = true
			view.updated = now
		}
		return
	}
	if view == nil {
		if preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 0 && strings.TrimSpace(preview.Input) == "" &&
			slices.ContainsFunc(p.Order, func(id string) bool {
				old := p.Views[id]
				return old.Complete && (len(old.Current.Files) != 0 || old.Current.DiffText)
			}) {
			// A new call's empty patch header is only a placeholder. Keep the
			// last useful card until this call has a projected change.
			return
		}
		if p.Views == nil {
			p.Views = make(map[string]*PreviewView)
		}
		view = &PreviewView{}
		p.Order = append(p.Order, preview.ID)
		p.Views[preview.ID] = view
	}
	// Replayed completion may refresh content, but cannot restart its hold.
	if !view.Complete || !preview.Complete {
		view.updated = now
	}
	view.Current, view.Complete = preview, preview.Complete
	p.batch(preview.Caller)
}

// live counts calls whose input is still streaming.
func (p *PreviewPane) Live() int {
	count := 0
	for _, id := range p.Order {
		if !p.Views[id].Complete {
			count++
		}
	}
	return count
}

// callers counts distinct callers with a card, for the dock heading.
func (p *PreviewPane) Callers() []string {
	var names []string
	for _, id := range p.Order {
		if caller := p.Views[id].Current.Caller; !slices.Contains(names, caller) {
			if len(p.batch(caller).order) == 0 {
				continue
			}
			names = append(names, caller)
		}
	}
	return names
}

func (p *PreviewView) columns(width int) (diffrender.Numbers, int) {
	numbers := p.numbers.Fit(width)
	return numbers, max(1, width-4-numbers.Width())
}

// previewNumbers selects the coordinate columns: a file's existing sides
// from the start, so arriving rows cannot shift the source sideways.
// Unnumbered sources, such as diff tails, have none.
func previewNumbers(preview previewmodel.Preview, rows []PreviewRow) diffrender.Numbers {
	var numbers diffrender.Numbers
	if len(preview.Files) > 0 {
		numbers.Old, numbers.New = preview.Files[0].BeforePath != "", preview.Files[0].AfterPath != ""
	}
	oldDigits, newDigits := 0, 0
	for _, row := range rows {
		if row.Old > 0 {
			numbers.Old, oldDigits = true, max(oldDigits, len(strconv.Itoa(row.Old)))
		}
		if row.New > 0 {
			numbers.New, newDigits = true, max(newDigits, len(strconv.Itoa(row.New)))
		}
	}
	if numbers.Digits = max(oldDigits, newDigits); numbers.Digits == 0 {
		return diffrender.Numbers{}
	}
	return numbers
}

func liveDiffPreviewRows(review diffmodel.ReviewFile, workspace string) ([]PreviewRow, error) {
	if review.BeforePath != "" && review.AfterPath == "" {
		path := pathdisplay.ForWorkspace(workspace, review.BeforePath)
		return []PreviewRow{{Kind: ' ', Text: "# " + path + " deleted\n"}}, nil
	}
	hunks, err := review.Hunks()
	if err != nil {
		return nil, err
	}
	var rows []PreviewRow
	for _, hunk := range hunks {
		before, after := hunk.BeforeStart+1, hunk.AfterStart+1
		for _, row := range hunk.Rows {
			next := PreviewRow{Kind: row.Kind, Text: row.Text}
			if row.Kind != '+' {
				next.Old = before
				before++
			}
			if row.Kind != '-' {
				next.New = after
				after++
			}
			rows = append(rows, next)
		}
	}
	return rows, nil
}

// liveDiffPreviewChanged returns the replaced rows: after[start:end] took the
// place of before[start:oldEnd]. Rows match by content, so context that only
// renumbered under an added row is unchanged.
func liveDiffPreviewChanged(before, after []PreviewRow) (start, end, oldEnd int) {
	same := func(a, b PreviewRow) bool { return a.Kind == b.Kind && a.Text == b.Text }
	for start < len(before) && start < len(after) && same(before[start], after[start]) {
		start++
	}
	end, oldEnd = len(after), len(before)
	for end > start && oldEnd > start && same(after[end-1], before[oldEnd-1]) {
		end--
		oldEnd--
	}
	return start, end, oldEnd
}

// Locate the new end of the changed range, not the hunk's start. Trailing
// unchanged context must not steal focus from a growing multiline replacement.
func liveDiffPreviewFocus(before, after []PreviewRow) int {
	start, end, _ := liveDiffPreviewChanged(before, after)
	// A growing rewrite can add source before a large baseline suffix that
	// still appears deleted. Follow the arriving source, not that old suffix.
	for i := end - 1; i >= start; i-- {
		if after[i].Kind == '+' {
			return i
		}
	}
	for i := end - 1; i >= start; i-- {
		if after[i].Kind == '+' || after[i].Kind == '-' {
			return i
		}
	}
	return max(0, min(start, len(after)-1))
}

func (p *PreviewView) prepare(now time.Time) error {
	current := p.Current
	if p.Rendered.ID == current.ID && p.Rendered.Input == current.Input && p.Rendered.DiffText == current.DiffText && slices.Equal(p.Rendered.Files, current.Files) {
		return nil
	}
	var source []PreviewRow
	var err error
	if current.Input != "" {
		if current.DiffText {
			for _, row := range diffrender.DiffRows(strings.TrimSuffix(current.Input, "\n")) {
				source = append(source, PreviewRow{Kind: row.Kind, Text: row.Text + "\n"})
			}
		} else {
			for i, line := range strings.Split(strings.TrimSuffix(current.Input, "\n"), "\n") {
				source = append(source, PreviewRow{New: i + 1, Kind: ' ', Text: line + "\n"})
			}
		}
	}
	if len(current.Files) > 0 {
		source, err = liveDiffPreviewRows(current.Files[0], current.Workspace)
		if err != nil {
			return err
		}
	}
	before := p.Source
	if p.Rendered.ID != current.ID {
		before = nil
	}
	p.Focus = liveDiffPreviewFocus(before, source)
	p.born = PreviewBirths(before, source, p.born, now)
	if current.Input != "" && !current.DiffText {
		p.Focus = max(0, len(source)-1)
	}
	if !p.Paused {
		p.ScrollRow = 0 // New source releases the preceding scroll-to-bottom anchor.
	}
	p.Source, p.Rendered = source, current
	p.numbers = p.numbers.Union(previewNumbers(current, source))
	return nil
}

// PreviewBirths carries reveal times across a snapshot. Unchanged rows
// and rows that only grew keep theirs, so a streaming line does not restart
// its fade; newly revealed rows cascade in order.
func PreviewBirths(before, after []PreviewRow, born []time.Time, now time.Time) []time.Time {
	if len(born) != len(before) {
		born = make([]time.Time, len(before))
	}
	next := make([]time.Time, len(after))
	start, end, oldEnd := liveDiffPreviewChanged(before, after)
	shift := 0 // A clipped tail slides and renumbers its rows.
	if start == 0 && len(before) > 0 {
		for shift = 1; shift < len(before); shift++ {
			if n := min(len(before)-shift, len(after)); liveDiffPreviewOverlap(before[shift:shift+n], after[:n]) {
				start, end, oldEnd = 0, len(after), len(before)
				break
			}
		}
		shift %= len(before)
	}
	copy(next[end:], born[oldEnd:])
	step := liveDiffPreviewStagger
	if count := time.Duration(end - start); count > 0 {
		step = min(step, PreviewMaxStagger/count)
	}
	fresh := time.Duration(0)
	for i := range end {
		// Replaced rows pair up in order; a row that only grew keeps its time.
		if old := i + shift; i < start || old < oldEnd && liveDiffPreviewOverlap(before[old:old+1], after[i:i+1]) {
			next[i] = born[old]
			continue
		}
		next[i] = now.Add(fresh * step)
		fresh++
	}
	return next
}

// liveDiffPreviewOverlap matches rows by content, letting the last one grow.
func liveDiffPreviewOverlap(before, after []PreviewRow) bool {
	if len(before) != len(after) || len(before) == 0 {
		return false
	}
	last := len(before) - 1
	for i := range last {
		if before[i].Kind != after[i].Kind || before[i].Text != after[i].Text {
			return false
		}
	}
	return before[last].Kind == after[last].Kind &&
		strings.HasPrefix(after[last].Text, strings.TrimSuffix(before[last].Text, "\n"))
}

// liveDiffPreviewEase decelerates a fade so rows settle rather than stop.
func liveDiffPreviewEase(progress float64) float64 {
	progress = 1 - min(1, max(0, progress))
	return 1 - progress*progress*progress
}

// Render and color only a bounded source window around the streaming tip.
// Captured history composition stays out of this high-frequency path.
func (p *PreviewView) Render(ctx context.Context, workspace string, theme diffrender.Theme, width, height int, motion PreviewMotion) ([]string, error) {
	if height <= 0 || p.Current.ID == "" {
		return nil, nil
	}
	now := motion.Now
	if err := p.prepare(now); err != nil {
		return nil, err
	}
	header := ansi.Truncate(diffrender.Gutter(false, theme)+p.Title(workspace, theme, width-3), max(0, width-1), "")
	lines := []string{header}
	rows := height - 1
	var footer []string
	if p.Current.Footer != "" && rows > 0 {
		footer = []string{ansi.Truncate(textsanitize.Safe(p.Current.Footer, false), max(0, width-1), "…")}
		rows--
	}
	finish := func(lines []string) []string {
		if len(footer) > 0 {
			for len(lines) < height-len(footer) {
				lines = append(lines, "")
			}
		}
		return append(lines, footer...)
	}
	if rows == 0 || len(p.Source) == 0 {
		return finish(lines), nil
	}
	numbers, sourceWidth := p.columns(width)
	fragmentsAt := func(i int) int {
		return diffrender.WrapRows(textsanitize.Safe(strings.TrimSuffix(p.Source[i].Text, "\n"), false), sourceWidth)
	}
	// Keep the tip's last wrapped fragment visible before admitting trailing
	// context. Fill the region with source rather than empty centering padding.
	focus := p.Focus
	if p.Paused || p.ScrollRow > focus {
		focus = min(max(0, p.ScrollRow), len(p.Source)-1)
	}
	p.rows = rows
	start, skip, count := focus, 0, 0
	// Following needs only nearby removed source, not a screenful of the
	// baseline being replaced: each deletion run keeps the rows nearest the
	// tip, and one marker row counts the rest. Manual scrolling still exposes
	// every row.
	type span struct{ from, to int }
	var hidden []span
	compact := !p.Paused && p.Source[focus].Kind == '+'
	run := 0
	for i := focus; i >= 0 && count < rows; i-- {
		if compact && p.Source[i].Kind == '-' {
			if run++; run > liveDiffPreviewKeptDeletions {
				from := i
				for from > 0 && p.Source[from-1].Kind == '-' {
					from--
				}
				hidden = append(hidden, span{from, i + 1})
				start, skip, i, run = from, 0, from, 0
				count++
				continue
			}
		} else {
			run = 0
		}
		n := fragmentsAt(i)
		start = i
		skip = max(0, n-(rows-count))
		count += n - skip
	}
	end := focus + 1
	run = 0
	for end < len(p.Source) && count < rows {
		if compact && p.Source[end].Kind == '-' {
			if run++; run > liveDiffPreviewKeptDeletions {
				to := end
				for to < len(p.Source) && p.Source[to].Kind == '-' {
					to++
				}
				hidden = append(hidden, span{end, to})
				end, run = to, 0
				count++
				continue
			}
		} else {
			run = 0
		}
		count += fragmentsAt(end)
		end++
	}
	hiddenAt := func(i int) (span, bool) {
		for _, h := range hidden {
			if h.from <= i && i < h.to {
				return h, true
			}
		}
		return span{}, false
	}
	// A little leading context improves multiline token state without lexing
	// a growing whole file on every frame.
	colorStart := max(0, start-32)
	visible := make([]int, 0, end-colorStart)
	for i := colorStart; i < end; i++ {
		if h, ok := hiddenAt(i); ok {
			i = h.to - 1
			continue
		}
		visible = append(visible, i)
	}
	source := make([]diffmodel.ReviewRow, 0, len(visible))
	for _, i := range visible {
		source = append(source, diffmodel.ReviewRow{Kind: p.Source[i].Kind, Text: p.Source[i].Text})
	}
	review := diffmodel.ReviewFile{BeforePath: "stream.sh", AfterPath: "stream.sh"}
	if len(p.Current.Files) > 0 {
		review = p.Current.Files[0]
	}
	var before, after []string
	var err error
	if p.Current.Input != "" && p.Current.DiffText && len(p.Current.Files) == 0 {
		// Input is a bounded unified tail. Parse its headers before selecting
		// the window so an off-screen header still selects the source lexer.
		colored, colorErr := p.renderer.ColorDiffRows(ctx, theme, "", textsanitize.Safe(strings.TrimSuffix(p.Current.Input, "\n"), false))
		if colorErr != nil {
			return nil, colorErr
		}
		for k, i := range visible {
			source[k] = colored[i]
		}
		for i := range source {
			source[i].Text = textsanitize.Safe(source[i].Text, true)
			if source[i].Kind == 0 {
				if strings.HasPrefix(source[i].Text, "--- ") || strings.HasPrefix(source[i].Text, "+++ ") {
					source[i].Text = diffrender.Subtle + source[i].Text[:4] + diffrender.SubtleReset + terminalui.StyledPath(source[i].Text[4:])
				} else {
					source[i].Text = diffrender.Subtle + source[i].Text + diffrender.SubtleReset
				}
			}
		}
	} else {
		if p.Current.Input != "" && len(p.Current.Files) == 0 {
			// Pending scope lists are plain text, not diffs.
			for _, row := range source {
				after = append(after, textsanitize.Safe(strings.TrimSuffix(row.Text, "\n"), false))
			}
			before = after
		} else {
			before, after, err = p.renderer.ColorHunk(ctx, theme, review, source)
		}
		if err != nil {
			return nil, err
		}
		diffmodel.AlignRows(source, before, after)
	}

	k := 0
	for i := colorStart; i < end && len(lines) <= rows; i++ {
		if h, ok := hiddenAt(i); ok {
			if i >= start {
				noun := "lines"
				if h.to-h.from == 1 {
					noun = "line"
				}
				marker := diffrender.Subtle + fmt.Sprintf("… %d more deleted %s", h.to-h.from, noun) + diffrender.SubtleReset
				line := diffrender.Gutter(false, theme) + diffrender.SourceLine(theme, width, numbers.Continuation(), marker, ' ')
				lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
			}
			i = h.to - 1
			continue
		}
		row := p.Source[i]
		text := source[k].Text
		k++
		if i < start {
			continue
		}
		rowNumbers := numbers.Format(row.Old, row.New)
		for n, fragment := range diffrender.WrapSource(text, sourceWidth) {
			if i == start && n < skip {
				continue
			}
			if len(lines) > rows {
				break
			}
			prefix := rowNumbers
			if n > 0 {
				prefix = numbers.Continuation()
			}
			// Only changed text fades in. Context, chrome, and row fills appear
			// settled. Completion can replace the streaming source with its
			// final diff; that is a settled snapshot, not newly arriving rows.
			changed := row.Kind == '+' || row.Kind == '-' || p.Current.Input != ""
			if until := p.born[i].Add(liveDiffPreviewFade); changed && motion.Enabled && !p.Complete && until.After(motion.Now) {
				progress := float64(motion.Now.Sub(p.born[i])) / float64(liveDiffPreviewFade)
				progress = liveDiffPreviewFadeFloor + (1-liveDiffPreviewFadeFloor)*liveDiffPreviewEase(progress)
				fragment = diffrender.Fade(fragment, progress, theme.TextCanvas(row.Kind, motion.Canvas))
			}
			kind := row.Kind
			if kind == 0 {
				kind = ' ' // Metadata has no source marker.
			}
			line := diffrender.Gutter(i == p.Focus, theme) + diffrender.SourceLine(theme, width, prefix, fragment, kind)
			lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
		}
	}
	return finish(lines), nil
}

// title follows the agents roster: a state glyph, then what the card shows.
// Edits use the file navigator's status letter and live line counts.
func (p *PreviewView) Title(workspace string, theme diffrender.Theme, width int) string {
	glyph := theme.Warning() + "◐" + terminalui.Reset
	var label, filePath, fileStats string
	var fileStatus diffmodel.Status
	reason, unavailable := strings.CutPrefix(p.Current.Status, previewmodel.PreviewUnavailable)
	switch {
	case unavailable:
		glyph, label = theme.Danger()+"!"+terminalui.Reset, textsanitize.Safe(reason, false)
	case len(p.Current.Files) > 0:
		file := p.Current.Files[0]
		path := file.AfterPath
		if path == "" {
			path = file.BeforePath
		}
		added, removed := file.LineCounts()
		filePath, fileStatus = pathdisplay.ForWorkspace(workspace, path), diffmodel.StatusOf(file)
		fileStats = diffrender.CountStats(diffmodel.Counts{Added: added, Removed: removed}, theme)
	case p.Current.Status == previewmodel.PreviewPending:
		label = "scoped effects"
	default:
		label = "edit"
	}
	if p.Current.Status == previewmodel.PreviewRunning && !p.Complete {
		label += " " + diffrender.Subtle + "· observed so far" + diffrender.SubtleReset
	}
	if p.Current.Input != "" && p.Current.Truncated {
		label += " " + diffrender.Subtle + "· tail" + diffrender.SubtleReset
	}
	if p.Complete && !unavailable {
		// Input completion keeps predictions provisional. Running observations
		// can finish when the projected file effect is seen, independently of
		// command success; neither case earns a success checkmark.
		glyph = terminalui.Dim + "○" + terminalui.Reset
		if p.Current.Status == previewmodel.PreviewRunning {
			label += " " + diffrender.Subtle + "· observed" + diffrender.SubtleReset
		} else {
			label += " " + diffrender.Subtle + "· preview" + diffrender.SubtleReset
		}
	}
	suffix := label
	if filePath != "" {
		label = diffrender.FileLabel(fileStatus, filePath, workspace, theme, width) + fileStats + suffix
	}
	label = glyph + " " + label
	caller := pathdisplay.AgentDisplayName(p.Current.Caller)
	if caller == "" {
		caller = p.Current.Thread
	}
	if caller == "" {
		caller = "unknown caller"
	}
	// Put attribution first so narrow panes do not silently lose the caller.
	// The caller keeps the agents pane's color for the same canonical path.
	caller = ansi.Truncate(textsanitize.Safe(caller, false), max(1, width/3, width-6-ansi.StringWidth(label)), "…")
	if filePath != "" {
		room := width - ansi.StringWidth(caller) - 3 - 2 - ansi.StringWidth(fileStats+suffix)
		label = glyph + " " + diffrender.FileLabel(fileStatus, filePath, workspace, theme, room) + fileStats + suffix
	}
	if color := terminalui.AgentColor(theme, p.Current.Caller); color != "" {
		caller = color + caller + "\x1b[0m"
	}
	return theme.Accent() + caller + "\x1b[0m" + theme.Accent() + " · \x1b[0m" + label + "\x1b[0m"
}
