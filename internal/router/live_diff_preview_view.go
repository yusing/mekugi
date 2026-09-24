package router

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
)

const (
	liveDiffPreviewFrameDelay = 33 * time.Millisecond
	// A finished card idle this long yields its slot when another call needs room.
	liveDiffPreviewStaleAfter = 10 * time.Second
	// Revealed rows fade in; rows revealed together cascade within a bound.
	liveDiffPreviewFade       = 180 * time.Millisecond
	liveDiffPreviewStagger    = 24 * time.Millisecond
	liveDiffPreviewMaxStagger = 160 * time.Millisecond
)

// Streaming has its own viewport and lifecycle. It never changes the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type liveDiffPreviewPane struct {
	views  map[string]*liveDiffPreviewView
	order  []string
	motion liveDiffPreviewMotion
}

// Motion is display-only. Without it, rows appear at their final colors.
type liveDiffPreviewMotion struct {
	enabled bool
	canvas  livediff.Canvas
	now     time.Time
}

// Each call owns its source window, syntax cache, and completion state.
type liveDiffPreviewView struct {
	current   liveDiffPreview
	complete  bool
	completed time.Time
	displayed bool
	digits    int // Line-number width only grows, so the source never shifts sideways.
	rendered  liveDiffPreview
	focus     int
	file      int
	renderer  liveDiffRenderer
	source    []liveDiffPreviewRow
	born      []time.Time // When each source row was revealed, for its fade.
	fading    time.Time   // Until a displayed row finishes fading in.
	paths     []liveDiffSourceSpan
}

type liveDiffPreviewRow struct {
	number int
	kind   byte
	text   string
}

func (p *liveDiffPreviewPane) update(preview liveDiffPreview) {
	view := p.views[preview.ID]
	if preview.Workspace != "" && preview.Status == "" && preview.Input == "" && len(preview.Files) == 0 {
		delete(p.views, preview.ID)
		p.order = slices.DeleteFunc(p.order, func(id string) bool { return id == preview.ID })
		return
	}
	if preview.Workspace == "" {
		if view != nil && !view.complete {
			view.complete, view.completed = true, time.Now()
		}
		return
	}
	if view == nil {
		// An evaluated completion must get a render opportunity before the next
		// fast call replaces it.
		replaceable := func(id string) bool {
			return p.views[id].complete && (!p.views[id].current.Evaluated || p.views[id].displayed)
		}
		// A new call takes over a finished card's slot, preferring its caller's,
		// so the other cards keep their positions and heights. Cards resize only
		// when concurrency grows or a stale finished card is dropped.
		slot := slices.IndexFunc(p.order, func(id string) bool {
			return replaceable(id) && p.views[id].current.Caller == preview.Caller
		})
		if slot < 0 {
			slot = slices.IndexFunc(p.order, replaceable)
		}
		if slot < 0 && len(p.order) >= 16 {
			// Capacity still favors new calls over old completions.
			slot = slices.IndexFunc(p.order, func(id string) bool { return p.views[id].complete })
			if slot < 0 {
				return
			}
		}
		if p.views == nil {
			p.views = make(map[string]*liveDiffPreviewView)
		}
		view = &liveDiffPreviewView{}
		if slot >= 0 {
			delete(p.views, p.order[slot])
			p.order[slot] = preview.ID
		} else {
			p.order = append(p.order, preview.ID)
		}
		p.views[preview.ID] = view
		now := time.Now()
		p.order = slices.DeleteFunc(p.order, func(id string) bool {
			if id == preview.ID || !replaceable(id) || now.Sub(p.views[id].completed) < liveDiffPreviewStaleAfter {
				return false
			}
			delete(p.views, id)
			return true
		})
	}
	if preview.Complete && !view.complete {
		view.completed = time.Now()
	}
	view.current, view.complete = preview, preview.Complete
}

// live counts calls whose input is still streaming.
func (p *liveDiffPreviewPane) live() int {
	count := 0
	for _, id := range p.order {
		if !p.views[id].complete {
			count++
		}
	}
	return count
}

// Finished cards leave after a brief hold without waiting for another tool
// call. Active cards are never aged out.
func (p *liveDiffPreviewPane) expire(now time.Time) bool {
	old := len(p.order)
	p.order = slices.DeleteFunc(p.order, func(id string) bool {
		view := p.views[id]
		if !view.complete || now.Sub(view.completed) < liveDiffPreviewStaleAfter {
			return false
		}
		delete(p.views, id)
		return true
	})
	return len(p.order) != old
}

func (p *liveDiffPreviewPane) nextExpiry(now time.Time) time.Duration {
	var next time.Duration
	for _, id := range p.order {
		view := p.views[id]
		if !view.complete {
			continue
		}
		remaining := max(time.Millisecond, liveDiffPreviewStaleAfter-now.Sub(view.completed))
		if next == 0 || remaining < next {
			next = remaining
		}
	}
	return next
}

// animating reports whether a displayed row is still fading in.
func (p *liveDiffPreviewPane) animating(now time.Time) bool {
	if !p.motion.enabled {
		return false
	}
	for _, id := range p.order {
		if p.views[id].fading.After(now) {
			return true
		}
	}
	return false
}

func (p *liveDiffPreviewPane) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || len(p.order) == 0 {
		return nil, nil
	}
	if p.motion.enabled {
		p.motion.now = time.Now()
	}
	// Give every visible call a heading and at least one source row. Reserve a
	// summary when a tiny terminal cannot show all calls, rather than cycling
	// callers on every delta. Expanding the terminal reveals the remaining calls.
	count := len(p.order)
	shown := count
	summary := 0
	if height < count*2 && count > 1 {
		shown = max(0, (height-1)/2)
		summary = 1
	}
	var lines []string
	for i, id := range p.order[:shown] {
		rows := (height - summary - len(lines)) / (shown - i)
		part, err := p.views[id].render(ctx, workspace, theme, width, rows, p.motion)
		if err != nil {
			return nil, err
		}
		p.views[id].displayed = len(part) > 1 || len(part) > 0 && len(p.views[id].source) == 0
		lines = append(lines, part...)
		// Stable card positions even when a call has little source so far.
		if i+1 < shown || summary > 0 {
			for range rows - len(part) {
				lines = append(lines, "")
			}
		}
	}
	if summary > 0 {
		label := fmt.Sprintf("STREAMING · +%d more calls · enlarge pane", count-shown)
		lines = append(lines, ansi.Truncate(theme.Accent()+label+"\x1b[0m", max(0, width-1), ""))
	}
	return lines, nil
}

func (p *liveDiffPreviewView) columns(width int) (digits, sourceWidth int) {
	if len(p.source) > 0 && p.source[len(p.source)-1].number > 0 {
		p.digits = max(p.digits, len(strconv.Itoa(p.source[len(p.source)-1].number)))
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

func liveDiffPreviewRows(review mekugi.ReviewFile, workspace string) ([]liveDiffPreviewRow, error) {
	if review.BeforePath != "" && review.AfterPath == "" {
		path := pathdisplay.ForWorkspace(workspace, review.BeforePath)
		return []liveDiffPreviewRow{{kind: ' ', text: "# " + path + " deleted\n"}}, nil
	}
	hunks, err := review.Hunks()
	if err != nil {
		return nil, err
	}
	var rows []liveDiffPreviewRow
	for _, hunk := range hunks {
		before, after := hunk.BeforeStart+1, hunk.AfterStart+1
		for _, row := range hunk.Rows {
			number := after
			if row.Kind == '-' {
				number = before
			}
			rows = append(rows, liveDiffPreviewRow{number, row.Kind, row.Text})
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
// place of before[start:oldEnd].
func liveDiffPreviewChanged(before, after []liveDiffPreviewRow) (start, end, oldEnd int) {
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	end, oldEnd = len(after), len(before)
	for end > start && oldEnd > start && after[end-1] == before[oldEnd-1] {
		end--
		oldEnd--
	}
	return start, end, oldEnd
}

// Locate the new end of the changed range, not the hunk's start. Trailing
// unchanged context must not steal focus from a growing multiline replacement.
func liveDiffPreviewFocus(before, after []liveDiffPreviewRow) int {
	start, end, _ := liveDiffPreviewChanged(before, after)
	for i := end - 1; i >= start; i-- {
		if after[i].kind == '+' || after[i].kind == '-' {
			return i
		}
	}
	return max(0, min(start, len(after)-1))
}

func (p *liveDiffPreviewView) prepare(now time.Time) error {
	current := p.current
	if p.rendered.ID == current.ID && p.rendered.Input == current.Input && slices.Equal(p.rendered.Syntax, current.Syntax) && slices.Equal(p.rendered.Files, current.Files) {
		return nil
	}
	file := min(p.file, max(0, len(current.Files)-1))
	for i, review := range current.Files {
		if p.rendered.ID != current.ID || !slices.Contains(p.rendered.Files, review) {
			file = i
		}
	}
	var source []liveDiffPreviewRow
	var err error
	if current.Input != "" {
		for i, line := range strings.Split(strings.TrimSuffix(current.Input, "\n"), "\n") {
			number := i + 1
			if current.DiffText {
				number = 0 // A clipped unified-diff row is not a source coordinate.
			}
			source = append(source, liveDiffPreviewRow{number, ' ', line + "\n"})
		}
	}
	if len(current.Files) > 0 {
		source, err = liveDiffPreviewRows(current.Files[file], current.Workspace)
		if err != nil {
			return err
		}
	}
	before := p.source
	if p.rendered.ID != current.ID || p.file != file {
		before = nil
	}
	p.focus = liveDiffPreviewFocus(before, source)
	p.born = liveDiffPreviewBirths(before, source, p.born, now)
	if current.Input != "" {
		p.focus = max(0, len(source)-1)
	}
	p.file, p.source, p.rendered = file, source, current
	p.paths = nil
	if current.Input != "" && !current.DiffText {
		syntax := current.Syntax
		if len(syntax) == 0 {
			syntax = liveDiffScriptSyntax(current.Input)
		}
		p.paths = liveDiffSourceRows(current.Input, syntax)
	}
	return nil
}

// liveDiffPreviewBirths carries reveal times across a snapshot. Unchanged rows
// and rows that only grew keep theirs, so a streaming line does not restart
// its fade; newly revealed rows cascade in order.
func liveDiffPreviewBirths(before, after []liveDiffPreviewRow, born []time.Time, now time.Time) []time.Time {
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
		step = min(step, liveDiffPreviewMaxStagger/count)
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
func liveDiffPreviewOverlap(before, after []liveDiffPreviewRow) bool {
	if len(before) != len(after) || len(before) == 0 {
		return false
	}
	last := len(before) - 1
	for i := range last {
		if before[i].kind != after[i].kind || before[i].text != after[i].text {
			return false
		}
	}
	return before[last].kind == after[last].kind &&
		strings.HasPrefix(after[last].text, strings.TrimSuffix(before[last].text, "\n"))
}

// liveDiffPreviewEase decelerates a fade so rows settle rather than stop.
func liveDiffPreviewEase(progress float64) float64 {
	progress = 1 - min(1, max(0, progress))
	return 1 - progress*progress*progress
}

// Render and color only a bounded source window around the streaming tip.
// Captured history composition stays out of this high-frequency path.
func (p *liveDiffPreviewView) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int, motion liveDiffPreviewMotion) ([]string, error) {
	if height <= 0 || p.current.ID == "" {
		return nil, nil
	}
	now := motion.now
	if !motion.enabled {
		now = time.Now()
	}
	if err := p.prepare(now); err != nil {
		return nil, err
	}
	p.fading = time.Time{}
	title := p.current.Status
	if title == "" {
		title = "STREAMING PREVIEW"
	}
	if p.current.Input != "" && !p.current.DiffText && !strings.HasPrefix(title, "RUNNING") && !strings.HasPrefix(title, "PENDING") {
		title = "STREAMING SCRIPT"
	}
	if p.complete && !p.current.Evaluated {
		title = "STREAMING COMPLETE"
		if qualification, ok := strings.CutPrefix(p.current.Status, "STREAMING PREVIEW: "); ok {
			title += " · " + qualification
		}
	}
	if p.current.Input != "" && p.current.Truncated {
		title += " · tail"
	}
	if strings.HasPrefix(p.current.Status, "PREVIEW UNAVAILABLE:") {
		title = p.current.Status
	}
	if len(p.current.Files) > 0 {
		file := p.current.Files[p.file]
		path := file.AfterPath
		if path == "" {
			path = file.BeforePath
		}
		title += " · " + pathdisplay.ForWorkspace(workspace, path)
	}
	caller := p.current.Caller
	if caller == "" {
		caller = p.current.Thread
	}
	if caller == "" {
		caller = "unknown caller"
	}
	// Put attribution first so narrow panes do not silently lose the caller.
	// The caller keeps the agents pane's color for the same canonical path.
	safeTitle := livediff.Safe(title, false)
	caller = ansi.Truncate(livediff.Safe(caller, false), max(1, width/3, width-6-ansi.StringWidth(safeTitle)), "…")
	if color := liveAgentColor(p.current.Caller); color != "" {
		caller = color + caller + "\x1b[0m" + theme.Accent()
	}
	header := ansi.Truncate(livediff.Gutter(false, theme)+theme.Accent()+caller+" · "+safeTitle+"\x1b[0m", max(0, width-1), "")
	lines := []string{header}
	rows := height - 1
	var footer []string
	if p.current.Footer != "" && rows > 0 {
		footer = []string{ansi.Truncate(livediff.Safe(p.current.Footer, false), max(0, width-1), "…")}
		rows--
	}
	if rows == 0 || len(p.source) == 0 {
		return append(lines, footer...), nil
	}
	digits, sourceWidth := p.columns(width)
	fragmentsAt := func(i int) int {
		text := livediff.Safe(strings.TrimSuffix(p.source[i].text, "\n"), false)
		return strings.Count(ansi.Hardwrap(text, sourceWidth, true), "\n") + 1
	}
	// Keep the tip's last wrapped fragment visible before admitting trailing
	// context. Fill the region with source rather than empty centering padding.
	start, skip, count := p.focus, 0, 0
	for i := p.focus; i >= 0 && count < rows; i-- {
		n := fragmentsAt(i)
		start = i
		skip = max(0, n-(rows-count))
		count += n - skip
	}
	end := p.focus + 1
	for end < len(p.source) && count < rows {
		count += fragmentsAt(end)
		end++
	}
	// A little leading context improves multiline token state without lexing
	// a growing whole file on every frame.
	colorStart := max(0, start-32)
	source := make([]mekugi.ReviewRow, 0, end-colorStart)
	for _, row := range p.source[colorStart:end] {
		source = append(source, mekugi.ReviewRow{Kind: row.kind, Text: row.text})
	}
	review := mekugi.ReviewFile{BeforePath: "stream.sh", AfterPath: "stream.sh"}
	if len(p.current.Files) > 0 {
		review = p.current.Files[p.file]
	}
	var before, after []string
	var err error
	if p.current.DiffText {
		for _, row := range p.source[colorStart:end] {
			after = append(after, livediff.Safe(strings.TrimSuffix(row.text, "\n"), false))
		}
		before = after
	} else if p.current.Input != "" {
		after, err = p.colorScript(ctx, theme, colorStart, end)
		before = after
	} else {
		before, after, err = p.renderer.ColorHunk(ctx, theme, review, source)
	}

	if err != nil {
		return nil, err
	}
	oldIndex, newIndex := 0, 0
	for i := colorStart; i < end && len(lines) <= rows; i++ {
		row := p.source[i]
		text := ""
		if row.kind != '+' {
			text = before[oldIndex]
			oldIndex++
		}
		if row.kind != '-' {
			text = after[newIndex]
			newIndex++
		}
		if i < start {
			continue
		}
		numbers := ""
		if digits > 0 && width-3 >= digits+4 {
			numbers = fmt.Sprintf("\x1b[2m%*d│\x1b[22m", digits, row.number)
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
				prefix = "\x1b[2m" + strings.Repeat(" ", digits) + "│\x1b[22m"
			}
			line := livediff.Gutter(i == p.focus, theme) + livediff.SourceLine(theme, width, prefix, fragment, row.kind)
			line = ansi.Truncate(line, max(0, width-1), "")
			if until := p.born[i].Add(liveDiffPreviewFade); motion.enabled && until.After(motion.now) {
				if until.After(p.fading) {
					p.fading = until
				}
				progress := float64(motion.now.Sub(p.born[i])) / float64(liveDiffPreviewFade)
				line = livediff.Fade(line, liveDiffPreviewEase(progress), motion.canvas)
			}
			lines = append(lines, line)
		}
	}
	return append(lines, footer...), nil
}
