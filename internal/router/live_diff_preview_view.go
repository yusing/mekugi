package router

import (
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
// diff's selection, scroll, acknowledgements, horizontal offset, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type liveDiffPreviewPane struct {
	active   map[string]liveDiffPreview
	order    []string
	current  liveDiffPreview
	hideAt   time.Time
	rendered liveDiffPreview
	file     int
	focus    int
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

// Split the body, excluding the captured-diff header and the common footer.
// The preview's divider/title is part of its fixed 70% allocation.
func liveDiffRegionRows(body int, streaming bool) (diff, preview int) {
	if !streaming || body < 2 {
		return body, 0
	}
	diff = max(1, body*3/10)
	return diff, body - diff
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
	if p.rendered.ID == current.ID && slices.Equal(p.rendered.Files, current.Files) {
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
	p.file, p.source, p.rendered = file, source, current
	return nil
}

// Render only the fixed visible region. Full-hunk syntax lexing and captured
// history composition are intentionally absent from this high-frequency path.
func (p *liveDiffPreviewPane) render(workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || p.current.ID == "" {
		return nil, nil
	}
	if err := p.prepare(); err != nil {
		return nil, err
	}
	title := "STREAMING PREVIEW · not validated or applied"
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
	digits := len(strconv.Itoa(p.source[len(p.source)-1].number))
	// Keep the growing row visible, including its final wrapped fragment.
	// End at the focus itself: wrapped trailing context must never consume
	// the region before the changed row gets any space.
	end := min(len(p.source), p.focus+1)
	var visible [][]string
	count := 0
	for i := end - 1; i >= 0 && count < rows; i-- {
		row := p.source[i]
		numbers := fmt.Sprintf("\x1b[2m%*d│\x1b[22m", digits, row.number)
		if width-3 < digits+4 {
			numbers = ""
		}
		sourceWidth := max(1, width-4-ansi.StringWidth(numbers))
		text := liveDiffSafe(strings.TrimSuffix(row.text, "\n"), false)
		fragments := strings.Split(ansi.Hardwrap(text, sourceWidth, true), "\n")
		// Only retain visible fragments of a long source row.
		start := max(0, len(fragments)-(rows-count))
		var rendered []string
		for n := start; n < len(fragments); n++ {
			prefix := numbers
			if n > 0 && numbers != "" {
				prefix = "\x1b[2m" + strings.Repeat(" ", digits) + "│\x1b[22m"
			}
			line := liveDiffGutter(i == p.focus, theme) + liveDiffSourceLine(theme, width, prefix, fragments[n], row.kind)
			rendered = append(rendered, ansi.Truncate(line, max(0, width-1), ""))
		}
		count += len(rendered)
		visible = append(visible, rendered)
	}
	for i := len(visible) - 1; i >= 0; i-- {
		lines = append(lines, visible[i]...)
	}
	return lines, nil
}
