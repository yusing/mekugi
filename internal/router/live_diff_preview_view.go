package router

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const liveDiffPreviewFrameDelay = 33 * time.Millisecond
const liveDiffPreviewHideDelay = 1500 * time.Millisecond

// Streaming has its own viewport and lifecycle. It never changes the captured
// diff's selection, scroll, acknowledgements, or follow mode.
// Updates replace snapshots; only a displayed frame parses and lays out rows.
type liveDiffPreviewPane struct {
	views map[string]*liveDiffPreviewView
	order []string
}

// Each call owns its source window, syntax cache, and completion deadline.
type liveDiffPreviewView struct {
	current  liveDiffPreview
	hideAt   time.Time
	rendered liveDiffPreview
	focus    int
	renderer liveDiffRenderer
	source   []liveDiffPreviewRow
	paths    []liveDiffSourceSpan
}

type liveDiffPreviewRow struct {
	number int
	text   string
}

func (p *liveDiffPreviewPane) update(preview liveDiffPreview, now time.Time) {
	view := p.views[preview.ID]
	if preview.Workspace == "" {
		if view != nil && view.hideAt.IsZero() {
			view.hideAt = now.Add(liveDiffPreviewHideDelay)
		}
		return
	}
	if view == nil {
		// Completed cards must not crowd out a new live call or grow storage
		// beyond the broker's active-preview limit.
		p.order = slices.DeleteFunc(p.order, func(id string) bool {
			if p.views[id].hideAt.IsZero() {
				return false
			}
			delete(p.views, id)
			return true
		})
		if len(p.order) >= 16 {
			return
		}
		if p.views == nil {
			p.views = make(map[string]*liveDiffPreviewView)
		}
		view = &liveDiffPreviewView{}
		p.views[preview.ID] = view
		p.order = append(p.order, preview.ID)
	}
	view.current, view.hideAt = preview, time.Time{}
}

func (p *liveDiffPreviewPane) hideAt() time.Time {
	var next time.Time
	for _, view := range p.views {
		if !view.hideAt.IsZero() && (next.IsZero() || view.hideAt.Before(next)) {
			next = view.hideAt
		}
	}
	return next
}

func (p *liveDiffPreviewPane) expire(now time.Time) bool {
	before := len(p.order)
	p.order = slices.DeleteFunc(p.order, func(id string) bool {
		view := p.views[id]
		if !view.hideAt.IsZero() && !now.Before(view.hideAt) {
			delete(p.views, id)
			return true
		}
		return false
	})
	return len(p.order) != before
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
		lines = append(lines, ansi.Truncate(theme.accent()+label+"\x1b[0m", max(0, width-1), ""))
	}
	return lines, nil
}

// Streaming source uses a 7:3 captured-diff/preview split.
func liveDiffRegionRows(body, captured int, streaming bool) (diff, preview int) {
	if !streaming {
		return body, 0
	}
	if captured == 0 {
		return 0, body
	}
	if body < 2 {
		return body, 0
	}
	diff = min(captured, max(1, body*7/10))
	return diff, body - diff
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

func (p *liveDiffPreviewView) prepare() {
	current := p.current
	if p.rendered.ID == current.ID && p.rendered.Input == current.Input && slices.Equal(p.rendered.Syntax, current.Syntax) {
		return
	}
	p.source = nil
	if current.Input != "" {
		for i, line := range strings.Split(strings.TrimSuffix(current.Input, "\n"), "\n") {
			p.source = append(p.source, liveDiffPreviewRow{i + 1, line + "\n"})
		}
	}
	p.focus = max(0, len(p.source)-1)
	p.rendered = current
	p.paths = nil
	if current.Input != "" {
		syntax := current.Syntax
		if len(syntax) == 0 {
			syntax = liveDiffScriptSyntax(current.Input)
		}
		p.paths = liveDiffSourceRows(current.Input, syntax)
	}
}

// Render and color only a bounded source window around the streaming tip.
// Captured history composition stays out of this high-frequency path.
func (p *liveDiffPreviewView) render(ctx context.Context, workspace string, theme liveDiffTheme, width, height int) ([]string, error) {
	if height <= 0 || p.current.ID == "" {
		return nil, nil
	}
	p.prepare()
	title := "STREAMING SCRIPT"
	if !p.hideAt.IsZero() {
		title = "STREAMING COMPLETE"
	}
	if p.current.Input != "" && p.current.Truncated {
		title += " · tail"
	}
	if strings.HasPrefix(p.current.Status, "PREVIEW UNAVAILABLE:") {
		title = p.current.Status
	}
	caller := p.current.Caller
	if caller == "" {
		caller = p.current.Thread
	}
	if caller == "" {
		caller = "unknown caller"
	}
	// Put attribution first so narrow panes do not silently lose the caller.
	caller = ansi.Truncate(liveDiffSafe(caller, false), max(1, min(28, width/3)), "…")
	identity := caller + " · " + p.current.ID[:min(6, len(p.current.ID))]
	title = identity + " · " + title
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
	colored, err := p.colorScript(ctx, theme, colorStart, end)

	if err != nil {
		return nil, err
	}
	for i := colorStart; i < end && len(lines) <= rows; i++ {
		row := p.source[i]
		text := colored[i-colorStart]
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
			line := liveDiffGutter(i == p.focus, theme) + liveDiffSourceLine(theme, width, prefix, fragment, ' ')
			lines = append(lines, ansi.Truncate(line, max(0, width-1), ""))
		}
	}
	return lines, nil
}
