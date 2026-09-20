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

const liveDiffPreviewFrameDelay = 33 * time.Millisecond

// Streaming has its own viewport and lifecycle. It never changes the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type liveDiffPreviewPane struct {
	views map[string]*liveDiffPreviewView
	order []string
}

// Each call owns its source window, syntax cache, and completion state.
type liveDiffPreviewView struct {
	current   liveDiffPreview
	complete  bool
	displayed bool
	rendered  liveDiffPreview
	focus     int
	file      int
	renderer  liveDiffRenderer
	source    []liveDiffPreviewRow
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
		if view != nil {
			view.complete = true
		}
		return
	}
	if view == nil {
		// An evaluated completion must get a render opportunity before the next
		// fast call replaces it. Capacity still favors new calls over old completions.
		p.order = slices.DeleteFunc(p.order, func(id string) bool {
			if !p.views[id].complete || p.views[id].current.Evaluated && !p.views[id].displayed {
				return false
			}
			delete(p.views, id)
			return true
		})
		if len(p.order) >= 16 {
			evict := slices.IndexFunc(p.order, func(id string) bool { return p.views[id].complete })
			if evict < 0 {
				return
			}
			delete(p.views, p.order[evict])
			p.order = slices.Delete(p.order, evict, evict+1)
		}
		if p.views == nil {
			p.views = make(map[string]*liveDiffPreviewView)
		}
		view = &liveDiffPreviewView{}
		p.views[preview.ID] = view
		p.order = append(p.order, preview.ID)
	}
	view.current, view.complete = preview, preview.Complete
}

func (p *liveDiffPreviewPane) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || len(p.order) == 0 {
		return nil, nil
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
		part, err := p.views[id].render(ctx, workspace, theme, width, rows)
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
		digits = len(strconv.Itoa(p.source[len(p.source)-1].number))
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

// Locate the new end of the changed range, not the hunk's start. Trailing
// unchanged context must not steal focus from a growing multiline replacement.
func liveDiffPreviewFocus(before, after []liveDiffPreviewRow) int {
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	end, oldEnd := len(after), len(before)
	for end > start && oldEnd > start && after[end-1] == before[oldEnd-1] {
		end--
		oldEnd--
	}
	for i := end - 1; i >= start; i-- {
		if after[i].kind == '+' || after[i].kind == '-' {
			return i
		}
	}
	return max(0, min(start, len(after)-1))
}

func (p *liveDiffPreviewView) prepare() error {
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

// Render and color only a bounded source window around the streaming tip.
// Captured history composition stays out of this high-frequency path.
func (p *liveDiffPreviewView) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || p.current.ID == "" {
		return nil, nil
	}
	if err := p.prepare(); err != nil {
		return nil, err
	}
	title := p.current.Status
	if title == "" {
		title = "STREAMING PREVIEW"
	}
	if p.current.Input != "" && !p.current.DiffText {
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
	caller = ansi.Truncate(livediff.Safe(caller, false), max(1, min(28, width/3)), "…")
	title = caller + " · " + title
	header := ansi.Truncate(theme.Accent()+livediff.Safe(title, false)+"\x1b[0m", max(0, width-1), "")
	lines := []string{header}
	rows := height - 1
	if rows == 0 || len(p.source) == 0 {
		return lines, nil
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
			lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
		}
	}
	return lines, nil
}
