package diffview

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
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
)

// Previews have their own viewport and lifecycle. They never change the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type PreviewPane struct {
	Views   map[string]*PreviewView
	Order   []string
	Motion  PreviewMotion
	Retain  bool // Native edit batches retain completed calls until the caller settles.
	batches map[string]*previewBatch
	// A dock too short for every call keeps one card open and folds the rest
	// to their headings. Prefer names the caller whose card opens first.
	Prefer    string
	open      string
	openedAt  time.Time
	Accordion bool // The last frame folded cards.
	Compact   bool // Keep concurrent cards folded even when an even split fits.
}

// liveDiffDockRows is the least a card needs to be worth an even split: its
// heading and four source rows.
const liveDiffDockRows = 5

// liveDiffDockHold keeps an automatically opened card from yielding to
// another writer on every burst.
const liveDiffDockHold = 1500 * time.Millisecond

// Motion is display-only. Without it, rows appear at their final colors.
type PreviewMotion struct {
	Enabled bool
	Canvas  livediff.Canvas
	Now     time.Time
}

// Each call owns its source window, syntax cache, and completion state.
type PreviewView struct {
	Current   Preview
	Complete  bool
	displayed bool
	digits    int // Line-number width only grows, so the source never shifts sideways.
	Rendered  Preview
	Focus     int
	Paused    bool
	ScrollRow int
	rows      int
	file      int
	renderer  livediff.Renderer
	Source    []PreviewRow
	born      []time.Time // When each source row was revealed, for its fade.
	updated   time.Time   // The latest snapshot's arrival, for choosing the open card.
	Fading    time.Time   // Until a displayed row finishes fading in.
}

type PreviewRow struct {
	Number int
	Kind   byte
	Text   string
}

func (p *PreviewPane) Update(preview Preview) {
	view := p.Views[preview.ID]
	if preview.Workspace != "" && preview.Status == "" && preview.Input == "" && len(preview.Files) == 0 {
		delete(p.Views, preview.ID)
		p.Order = slices.DeleteFunc(p.Order, func(id string) bool { return id == preview.ID })
		return
	}
	if preview.Workspace == "" {
		if view != nil && !view.Complete {
			view.Complete = true
			view.updated = time.Now()
		}
		return
	}
	if view == nil {
		if preview.Status == PreviewEdit && len(preview.Files) == 0 && strings.TrimSpace(preview.Input) == "" &&
			slices.ContainsFunc(p.Order, func(id string) bool {
				old := p.Views[id]
				return old.Complete && (len(old.Current.Files) != 0 || old.Current.DiffText)
			}) {
			// A new call's empty patch header is only a placeholder. Keep the
			// last useful card until this call has a projected change.
			return
		}
		// An evaluated completion must get a render opportunity before the next
		// fast call replaces it.
		replaceable := func(id string) bool {
			return !p.Retain && p.Views[id].Complete && (!p.Views[id].Current.Evaluated || p.Views[id].displayed)
		}
		// A new call takes over a finished card's slot, preferring its caller's,
		// so the other cards keep their positions and heights. Cards resize only
		// when concurrency grows.
		slot := slices.IndexFunc(p.Order, func(id string) bool {
			return replaceable(id) && p.Views[id].Current.Caller == preview.Caller
		})
		if slot < 0 {
			slot = slices.IndexFunc(p.Order, replaceable)
		}
		if !p.Retain && slot < 0 && len(p.Order) >= 16 {
			// Capacity still favors new calls over old completions.
			slot = slices.IndexFunc(p.Order, func(id string) bool { return p.Views[id].Complete })
			if slot < 0 {
				return
			}
		}
		if p.Views == nil {
			p.Views = make(map[string]*PreviewView)
		}
		view = &PreviewView{}
		if slot >= 0 {
			delete(p.Views, p.Order[slot])
			p.Order[slot] = preview.ID
		} else {
			p.Order = append(p.Order, preview.ID)
		}
		p.Views[preview.ID] = view
	}
	view.Current, view.Complete, view.updated = preview, preview.Complete, time.Now()
	if p.Retain {
		p.batch(preview.Caller)
	}
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

// animating reports whether a displayed row is still fading in.
func (p *PreviewPane) Animating(now time.Time) bool {
	if !p.Motion.Enabled {
		return false
	}
	for _, id := range p.Order {
		if p.Views[id].Fading.After(now) {
			return true
		}
	}
	return false
}

func (p *PreviewPane) Render(ctx context.Context, workspace string, theme livediff.Theme, width, height int) ([]string, error) {
	if height <= 0 || len(p.Order) == 0 {
		return nil, nil
	}
	if p.Motion.Enabled {
		p.Motion.Now = time.Now()
	}
	count := len(p.Order)
	p.Accordion = count > 1 && (p.Compact || height < count*liveDiffDockRows)
	if p.Accordion {
		return p.renderAccordion(ctx, workspace, theme, width, height)
	}
	var lines []string
	for i, id := range p.Order {
		rows := (height - len(lines)) / (count - i)
		part, err := p.Views[id].Render(ctx, workspace, theme, width, rows, p.Motion)
		if err != nil {
			return nil, err
		}
		p.Views[id].displayed = len(part) > 1 || len(part) > 0 && len(p.Views[id].Source) == 0
		lines = append(lines, part...)
		// Stable card positions even when a call has little source so far.
		if i+1 < count {
			for range rows - len(part) {
				lines = append(lines, "")
			}
		}
	}
	return lines, nil
}

// renderAccordion keeps card order, so positions stay stable while one card
// holds the source rows and the others fold to their heading. When even the
// headings do not fit, the remainder is counted rather than cycled.
func (p *PreviewPane) renderAccordion(ctx context.Context, workspace string, theme livediff.Theme, width, height int) ([]string, error) {
	open := p.chooseOpen(p.Motion.Now)
	openAt := slices.Index(p.Order, open)
	shown := min(len(p.Order), max(1, height-(liveDiffDockRows-1)))
	start := 0
	if openAt >= shown {
		start = openAt - shown + 1
	}
	hidden := len(p.Order) - shown
	if hidden > 0 {
		shown = max(1, min(shown, height-liveDiffDockRows))
		start = min(start, openAt)
		if openAt >= start+shown {
			start = openAt - shown + 1
		}
		hidden = len(p.Order) - shown
	}
	var lines []string
	for _, id := range p.Order[start : start+shown] {
		view := p.Views[id]
		if id != open {
			lines = append(lines, ansi.Truncate(livediff.Gutter(false, theme)+livediff.Subtle+"▸"+livediff.SubtleReset+" "+view.Title(workspace, theme, width-2), max(0, width-1), ""))
			continue
		}
		rows := height - shown + 1
		if hidden > 0 {
			rows--
		}
		part, err := view.Render(ctx, workspace, theme, width, rows, p.Motion)
		if err != nil {
			return nil, err
		}
		view.displayed = len(part) > 1 || len(part) > 0 && len(view.Source) == 0
		lines = append(lines, part...)
		for range rows - len(part) {
			lines = append(lines, "")
		}
	}
	if hidden > 0 {
		label := fmt.Sprintf("+%d more calls", hidden)
		lines = append(lines, ansi.Truncate(theme.Accent()+label+"\x1b[0m", max(0, width-1), ""))
	}
	for _, id := range p.Order {
		if id != open {
			p.Views[id].displayed = true // A folded heading has been shown.
		}
	}
	return lines, nil
}

// chooseOpen picks the card that keeps its source rows: the preferred caller's,
// then the current one while it is still
// arriving, then the card that changed most recently.
func (p *PreviewPane) chooseOpen(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	exists := func(id string) bool { return id != "" && p.Views[id] != nil }
	if p.Prefer != "" {
		best := ""
		for _, id := range p.Order {
			if p.Views[id].Current.Caller == p.Prefer && (best == "" || p.Views[best].Complete && !p.Views[id].Complete) {
				best = id
			}
		}
		if best != "" {
			return p.setOpen(best, now)
		}
	}
	// The open card keeps its rows while it is still arriving, briefly after
	// opening and while its input keeps changing.
	if exists(p.open) && !p.Views[p.open].Complete &&
		(now.Sub(p.openedAt) < liveDiffDockHold || now.Sub(p.Views[p.open].updated) < liveDiffDockHold) {
		return p.open
	}
	latest := p.Order[len(p.Order)-1]
	for _, id := range p.Order {
		view, best := p.Views[id], p.Views[latest]
		if view.Complete == best.Complete && view.updated.After(best.updated) || best.Complete && !view.Complete {
			latest = id
		}
	}
	return p.setOpen(latest, now)
}

func (p *PreviewPane) setOpen(id string, now time.Time) string {
	if p.open != id {
		p.open, p.openedAt = id, now
	}
	return id
}

// callers counts distinct callers with a card, for the dock heading.
func (p *PreviewPane) Callers() []string {
	var names []string
	for _, id := range p.Order {
		if caller := p.Views[id].Current.Caller; !slices.Contains(names, caller) {
			names = append(names, caller)
		}
	}
	return names
}

func (p *PreviewView) columns(width int) (digits, sourceWidth int) {
	if len(p.Source) > 0 && p.Source[len(p.Source)-1].Number > 0 {
		p.digits = max(p.digits, len(strconv.Itoa(p.Source[len(p.Source)-1].Number)))
		digits = p.digits
	}
	numberWidth := 0
	if digits > 0 {
		numberWidth = digits + 1
	}
	if width-3 < digits+4 {
		numberWidth = 0
	}
	return digits, max(1, width-4-numberWidth)
}

func liveDiffPreviewRows(review mekugi.ReviewFile, workspace string) ([]PreviewRow, error) {
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
			number := after
			if row.Kind == '-' {
				number = before
			}
			rows = append(rows, PreviewRow{number, row.Kind, row.Text})
			if row.Kind != '+' {
				before++
			}
			if row.Kind != '-' {
				after++
			}
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
	for i := end - 1; i >= start; i-- {
		if after[i].Kind == '+' || after[i].Kind == '-' {
			return i
		}
	}
	return max(0, min(start, len(after)-1))
}

func (p *PreviewView) prepare(now time.Time) error {
	current := p.Current
	if p.Rendered.ID == current.ID && p.Rendered.Input == current.Input && slices.Equal(p.Rendered.Files, current.Files) {
		return nil
	}
	file := min(p.file, max(0, len(current.Files)-1))
	for i, review := range current.Files {
		if p.Rendered.ID != current.ID || !slices.Contains(p.Rendered.Files, review) {
			file = i
		}
	}
	var source []PreviewRow
	var err error
	if current.Input != "" {
		for i, line := range strings.Split(strings.TrimSuffix(current.Input, "\n"), "\n") {
			number := i + 1
			if current.DiffText {
				number = 0 // A clipped unified-diff row is not a source coordinate.
			}
			source = append(source, PreviewRow{number, ' ', line + "\n"})
		}
	}
	if len(current.Files) > 0 {
		source, err = liveDiffPreviewRows(current.Files[file], current.Workspace)
		if err != nil {
			return err
		}
	}
	before := p.Source
	if p.Rendered.ID != current.ID || p.file != file {
		before = nil
	}
	p.Focus = liveDiffPreviewFocus(before, source)
	p.born = PreviewBirths(before, source, p.born, now)
	if current.Input != "" {
		p.Focus = max(0, len(source)-1)
	}
	p.file, p.Source, p.Rendered = file, source, current
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
func (p *PreviewView) Render(ctx context.Context, workspace string, theme livediff.Theme, width, height int, motion PreviewMotion) ([]string, error) {
	if height <= 0 || p.Current.ID == "" {
		return nil, nil
	}
	now := motion.Now
	if !motion.Enabled {
		now = time.Now()
	}
	if err := p.prepare(now); err != nil {
		return nil, err
	}
	p.Fading = time.Time{}
	header := ansi.Truncate(livediff.Gutter(false, theme)+p.Title(workspace, theme, width), max(0, width-1), "")
	lines := []string{header}
	rows := height - 1
	var footer []string
	if p.Current.Footer != "" && rows > 0 {
		footer = []string{ansi.Truncate(livediff.Safe(p.Current.Footer, false), max(0, width-1), "…")}
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
	digits, sourceWidth := p.columns(width)
	fragmentsAt := func(i int) int {
		text := livediff.Safe(strings.TrimSuffix(p.Source[i].Text, "\n"), false)
		return strings.Count(ansi.Hardwrap(text, sourceWidth, true), "\n") + 1
	}
	// Keep the tip's last wrapped fragment visible before admitting trailing
	// context. Fill the region with source rather than empty centering padding.
	focus := p.Focus
	if p.Paused {
		focus = min(max(0, p.ScrollRow), len(p.Source)-1)
	}
	p.rows = rows
	start, skip, count := focus, 0, 0
	for i := focus; i >= 0 && count < rows; i-- {
		n := fragmentsAt(i)
		start = i
		skip = max(0, n-(rows-count))
		count += n - skip
	}
	end := focus + 1
	for end < len(p.Source) && count < rows {
		count += fragmentsAt(end)
		end++
	}
	// A little leading context improves multiline token state without lexing
	// a growing whole file on every frame.
	colorStart := max(0, start-32)
	source := make([]mekugi.ReviewRow, 0, end-colorStart)
	for _, row := range p.Source[colorStart:end] {
		source = append(source, mekugi.ReviewRow{Kind: row.Kind, Text: row.Text})
	}
	review := mekugi.ReviewFile{BeforePath: "stream.sh", AfterPath: "stream.sh"}
	if len(p.Current.Files) > 0 {
		review = p.Current.Files[p.file]
	}
	var before, after []string
	var err error
	if p.Current.Input != "" {
		// Raw diff tails and pending scope lists are plain text.
		for _, row := range source {
			after = append(after, livediff.Safe(strings.TrimSuffix(row.Text, "\n"), false))
		}
		before = after
	} else {
		before, after, err = p.renderer.ColorHunk(ctx, theme, review, source)
	}

	if err != nil {
		return nil, err
	}
	livediff.AlignHunkColors(source, before, after)
	for i := colorStart; i < end && len(lines) <= rows; i++ {
		row := p.Source[i]
		text := source[i-colorStart].Text
		if i < start {
			continue
		}
		numbers := ""
		if digits > 0 && width-3 >= digits+4 {
			numbers = fmt.Sprintf(livediff.Subtle+"%*d│"+livediff.SubtleReset, digits, row.Number)
		}
		carry := ""
		for n, fragment := range strings.Split(ansi.Hardwrap(text, sourceWidth, true), "\n") {
			fragment = carry + fragment
			if at := strings.LastIndex(fragment, "\x1b["); at >= 0 {
				if end := strings.IndexByte(fragment[at:], 'm'); end >= 0 {
					carry = fragment[at : at+end+1]
				}
			}
			if i == start && n < skip {
				continue
			}
			if len(lines) > rows {
				break
			}
			prefix := numbers
			if n > 0 && numbers != "" {
				prefix = livediff.Subtle + strings.Repeat(" ", digits) + "│" + livediff.SubtleReset
			}
			// Only changed text fades in. Context, chrome, and row fills appear
			// settled. Completion can replace the streaming source with its
			// final diff; that is a settled snapshot, not newly arriving rows.
			changed := row.Kind == '+' || row.Kind == '-' || p.Current.Input != ""
			if until := p.born[i].Add(liveDiffPreviewFade); changed && motion.Enabled && !p.Complete && until.After(motion.Now) {
				if until.After(p.Fading) {
					p.Fading = until
				}
				progress := float64(motion.Now.Sub(p.born[i])) / float64(liveDiffPreviewFade)
				progress = liveDiffPreviewFadeFloor + (1-liveDiffPreviewFadeFloor)*liveDiffPreviewEase(progress)
				fragment = livediff.Fade(fragment, progress, theme.TextCanvas(row.Kind, motion.Canvas))
			}
			line := livediff.Gutter(i == p.Focus, theme) + livediff.SourceLine(theme, width, prefix, fragment, row.Kind)
			lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
		}
	}
	return finish(lines), nil
}

// title follows the agents roster: a state glyph, then what the card shows.
// Edits use the file navigator's status letter and live line counts.
func (p *PreviewView) Title(workspace string, theme livediff.Theme, width int) string {
	glyph := activityui.Amber + "◐" + activityui.Reset
	var label string
	reason, unavailable := strings.CutPrefix(p.Current.Status, PreviewUnavailable)
	switch {
	case unavailable:
		glyph, label = activityui.Red+"!"+activityui.Reset, livediff.Safe(reason, false)
	case len(p.Current.Files) > 0:
		file := p.Current.Files[p.file]
		path := file.AfterPath
		if path == "" {
			path = file.BeforePath
		}
		added, removed := file.LineCounts()
		label = FileLabel(StatusOf(file), pathdisplay.ForWorkspace(workspace, path), workspace, theme) +
			CountStats(livediff.Counts{Added: added, Removed: removed}, theme)
		if len(p.Current.Files) > 1 {
			label += fmt.Sprintf(" "+livediff.Subtle+"%d/%d files"+livediff.SubtleReset, p.file+1, len(p.Current.Files))
		}
	case p.Current.Status == PreviewPending:
		label = "scoped effects"
	default:
		label = "edit"
	}
	if p.Current.Status == PreviewRunning && !p.Complete {
		label += " " + livediff.Subtle + "· observed so far" + livediff.SubtleReset
	}
	if p.Current.Input != "" && p.Current.Truncated {
		label += " " + livediff.Subtle + "· tail" + livediff.SubtleReset
	}
	if p.Complete && !unavailable {
		// Input completion keeps predictions provisional. Running observations
		// can finish when the projected file effect is seen, independently of
		// command success; neither case earns a success checkmark.
		glyph = activityui.Dim + "○" + activityui.Reset
		if p.Current.Status == PreviewRunning {
			label += " " + livediff.Subtle + "· observed" + livediff.SubtleReset
		} else {
			label += " " + livediff.Subtle + "· preview" + livediff.SubtleReset
		}
	}
	label = glyph + " " + label
	caller := activityui.AgentDisplayName(p.Current.Caller)
	if caller == "" {
		caller = p.Current.Thread
	}
	if caller == "" {
		caller = "unknown caller"
	}
	// Put attribution first so narrow panes do not silently lose the caller.
	// The caller keeps the agents pane's color for the same canonical path.
	caller = ansi.Truncate(livediff.Safe(caller, false), max(1, width/3, width-6-ansi.StringWidth(label)), "…")
	if color := activityui.Color(p.Current.Caller); color != "" {
		caller = color + caller + "\x1b[0m"
	}
	return theme.Accent() + caller + "\x1b[0m" + theme.Accent() + " · \x1b[0m" + label + "\x1b[0m"
}

// A manual stream scroll pauses each visible card; new input still replaces its
// snapshot, without moving its source window. r restores each card's live tip.
func (p *PreviewPane) Scroll(key byte) bool {
	_, _, ok := terminalui.PaneScroll(key, 0, 1, 1)
	if !ok {
		return false
	}
	for _, v := range p.Views {
		at := v.ScrollRow
		if !v.Paused {
			at = v.Focus
		}
		next, follow, _ := terminalui.PaneScroll(key, at, 1, len(v.Source))
		if key == ' ' {
			next = min(len(v.Source)-1, at+max(1, v.rows))
		}
		if key == 'b' {
			next = max(0, at-max(1, v.rows))
		}
		v.Paused = !follow
		v.ScrollRow = next
	}
	return true
}
