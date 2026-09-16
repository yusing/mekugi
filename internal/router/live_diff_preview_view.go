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
)

const liveDiffPreviewFrameDelay = 33 * time.Millisecond
const liveDiffPreviewHideDelay = 300 * time.Millisecond

// Streaming has its own viewport and lifecycle. It never changes the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type liveDiffPreviewPane struct {
	active   map[string]liveDiffPreview
	order    []string
	current  liveDiffPreview
	hideAt   time.Time
	rendered liveDiffPreview
	file     int
	focus    int
	renderer liveDiffRenderer
	source   []liveDiffPreviewRow
}

type liveDiffPreviewRow struct {
	number int
	kind   byte
	text   string
}

func (p *liveDiffPreviewPane) update(preview liveDiffPreview, now time.Time) {
	if preview.Workspace == "" {
		if _, exists := p.active[preview.ID]; !exists {
			return
		}
		delete(p.active, preview.ID)
		p.order = slices.DeleteFunc(p.order, func(id string) bool { return id == preview.ID })
		if len(p.order) == 0 {
			p.hideAt = now.Add(liveDiffPreviewHideDelay)
		} else if p.current.ID == preview.ID {
			p.current = p.active[p.order[len(p.order)-1]]
		}
		return
	}
	if p.active == nil {
		p.active = make(map[string]liveDiffPreview)
	}
	p.active[preview.ID] = preview
	p.order = slices.DeleteFunc(p.order, func(id string) bool { return id == preview.ID })
	p.order = append(p.order, preview.ID)
	p.current, p.hideAt = preview, time.Time{}
}

func (p *liveDiffPreviewPane) expire(now time.Time) bool {
	if !p.hideAt.IsZero() && !now.Before(p.hideAt) {
		*p = liveDiffPreviewPane{}
		return true
	}
	return false
}

// Cap preview height at 70% of the body, keeping at least one captured row.
// Short previews use less space; the divider/title counts toward their height.
func liveDiffRegionRows(body int, streaming bool) (diff, preview int) {
	if !streaming || body < 2 {
		return body, 0
	}
	diff = max(1, body*3/10)
	return diff, body - diff
}

// Measure only until the display cap. This runs at paint time, not for each
// incoming delta, and uses the same source-column geometry as rendering.
func (p *liveDiffPreviewPane) height(width, limit int) (int, error) {
	if limit <= 0 || p.current.ID == "" {
		return 0, nil
	}
	if err := p.prepare(); err != nil {
		return 0, err
	}
	_, sourceWidth := p.columns(width)
	height := 1
	for _, row := range p.source {
		if height >= limit {
			return limit, nil
		}
		text := liveDiffSafe(strings.TrimSuffix(row.text, "\n"), false)
		height += strings.Count(ansi.Hardwrap(text, sourceWidth, true), "\n") + 1
	}
	return min(height, limit), nil
}

func (p *liveDiffPreviewPane) columns(width int) (digits, sourceWidth int) {
	if len(p.source) > 0 {
		digits = len(strconv.Itoa(p.source[len(p.source)-1].number))
	}
	numberWidth := digits + 1
	if width-3 < digits+4 {
		numberWidth = 0
	}
	return digits, max(1, width-4-numberWidth)
}

func liveDiffPreviewRows(review mekugi.ReviewFile) ([]liveDiffPreviewRow, error) {
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

func (p *liveDiffPreviewPane) prepare() error {
	current := p.current
	if p.rendered.ID == current.ID && p.rendered.Input == current.Input && slices.Equal(p.rendered.Files, current.Files) {
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
			source = append(source, liveDiffPreviewRow{i + 1, ' ', line + "\n"})
		}
	}
	if len(current.Files) > 0 {
		source, err = liveDiffPreviewRows(current.Files[file])
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
	return nil
}

// Render and color only a bounded source window around the streaming tip.
// Captured history composition stays out of this high-frequency path.
func (p *liveDiffPreviewPane) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || p.current.ID == "" {
		return nil, nil
	}
	if err := p.prepare(); err != nil {
		return nil, err
	}
	title := "STREAMING PREVIEW"
	if p.current.Input != "" {
		title = "STREAMING SCRIPT"
		if p.current.Truncated {
			title += " · tail"
		}
	}
	if !p.hideAt.IsZero() {
		title = "STREAMING COMPLETE"
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
		title += " · " + liveDiffDisplayPath(workspace, path)
	}
	header := ansi.Truncate(theme.accent()+liveDiffSafe(title, false)+"\x1b[0m", max(0, width-1), "")
	lines := []string{header}
	rows := height - 1
	if rows == 0 || len(p.source) == 0 {
		return lines, nil
	}
	digits, sourceWidth := p.columns(width)
	fragmentsAt := func(i int) int {
		text := liveDiffSafe(strings.TrimSuffix(p.source[i].text, "\n"), false)
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
	before, after, err := p.renderer.colorHunk(ctx, theme, review, source)
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
		if width-3 >= digits+4 {
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
			line := liveDiffGutter(i == p.focus, theme) + liveDiffSourceLine(theme, width, prefix, fragment, row.kind)
			lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
		}
	}
	return lines, nil
}
