package terminalview

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Source: apps/mekugi/internal/router/live_diff_terminal.go:380:637@[fc0514ff]
// paneBack is what Esc returns to after Enter in the navigator: the list
// after opening a file ('f'), or the previous caller filter after picking a
// branch ('b').
type paneBack struct {
	kind   byte
	caller string
}

// escape handles a decoded Esc: it closes help or the filter, then undoes the
// last list action, then leaves the list.
func (c *Pane) escape() {
	n := &c.navigation
	back := c.back
	c.back = paneBack{}
	switch {
	case c.help || n.Filtering:
		c.back = back
		n.Filtering, n.Focused, c.help = false, false, false
	case back.kind == 'b' && n.Focused:
		c.filterCaller(back.caller)
	case back.kind == 'f' && !n.Focused:
		n.Hidden, n.Focused = false, true
	default:
		n.Focused = false
	}
	c.dirty = true
}

// pointNav tracks the navigator row under the pointer.
func (c *Pane) pointNav(row int, inNav bool, firstRow int) {
	n := &c.navigation
	count, top := len(n.Entries), n.Top
	if n.ChangesTab {
		count, top = len(n.Changes.Rows), n.Changes.Top
	}
	hover := 0
	if index := top + row - firstRow - 2; inNav && row >= firstRow+2 && index < count {
		hover = index + 1
	}
	before := [2]int{n.Hover, n.Changes.Hover}
	n.Hover, n.Changes.Hover = 0, 0
	if n.ChangesTab {
		n.Changes.Hover = hover
	} else {
		n.Hover = hover
	}
	c.dirty = c.dirty || before != [2]int{n.Hover, n.Changes.Hover}
}

// ClearHover forgets the pointed row when the pointer leaves the pane.
func (c *Pane) ClearHover() {
	shown := c.navigation.Hover != 0 || c.navigation.Changes.Hover != 0
	c.navigation.Hover, c.navigation.Changes.Hover = 0, 0
	c.dirty = c.dirty || shown
}

// Key handles one decoded terminal key. The attachment owns sequence timing.
func (c *Pane) Key(input string) bool {
	if input == "" {
		return false
	}
	if input == "\x1b" {
		c.escape()
		return false
	}
	key := input[len(input)-1]
	if key == 3 {
		return true
	}
	if strings.HasPrefix(input, "\x1b") {
		switch input {
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		case "\x1b[C", "\x1bOC":
			key = 'l'
		case "\x1b[D", "\x1bOD":
			key = 'h'
		case "\x1b[H", "\x1bOH", "\x1b[1~":
			key = 'g'
		case "\x1b[F", "\x1bOF", "\x1b[4~":
			key = 'G'
		case "\x1b[5~":
			key = 'b'
		case "\x1b[6~":
			key = ' '
		}
		c.navigation.Filtering = false
	}
	c.dirty = true
	if key == '?' && !c.navigation.Filtering {
		c.help = !c.help
		return false
	}
	if c.help {
		return false
	}
	if c.navigationKey(key) {
		return false
	}

	if c.scroll(key) {
		return false
	}
	switch key {
	case '{', '}':
		c.stepChange(map[byte]int{'{': -1, '}': 1}[key])
	case 'a':
		c.cycleCaller()
	case '0':
		c.filterCaller("")
	case 'n':
		c.stepFile(1)
	case 'p':
		c.stepFile(-1)
	case '[', ']':
		if key == ']' {
			for _, at := range c.rendering.Hunks {
				if at > c.offset {
					c.view.JumpTo(c.rendering, at)
					break
				}
			}
		} else {
			for _, at := range slices.Backward(c.rendering.Hunks) {
				if at < c.offset {
					c.view.JumpTo(c.rendering, at)
					break
				}
			}
		}

	}
	c.dirty = true
	return false
}

// Mouse uses pane-local one-based cells and the shared pointer action codes.
func (c *Pane) Mouse(action byte, row, column int) {
	if action == 0 || c.help {
		return
	}
	navWidth := c.navigation.Width(c.lastWidth)
	inNav := navWidth > 0 && column <= navWidth || navWidth == 0 && (c.stack > 0 && row <= c.stack || c.stack == 0 && c.navigation.Focused)
	firstRow, lastRow := 1, c.lastHeight
	if action == 'h' {
		c.pointNav(row, inNav && row >= firstRow && row <= lastRow, firstRow)
		return
	}
	if row < firstRow || row > lastRow {
		return
	}
	c.dirty = true
	if action == '\r' {
		c.navigation.Focused = inNav
		c.navigation.Filtering = false
	}
	if inNav && c.navigation.ChangesTab {
		l := &c.navigation.Changes
		if action == '\r' {
			if row == firstRow+1 {
				c.navigationKey('/')
				return
			}
			if index := l.Top + row - firstRow - 2; row >= firstRow+2 && index < len(l.Rows) {
				l.Cursor, c.navigation.Focused = index, true
				c.openChangeRow()
				c.navigation.Focused = true
			}
		} else {
			delta := 1
			if action == 'k' {
				delta = -1
			}
			l.Top = max(0, min(l.Top+delta, max(0, len(l.Rows)-max(1, c.navRows-2))))
		}
		return
	}
	if inNav {
		n := &c.navigation
		if action == '\r' {
			if row == firstRow+1 {
				c.navigationKey('/')
				return
			}
			index := n.Top + row - firstRow - 2
			if row >= firstRow+2 && index < len(n.Entries) {
				n.Cursor, n.Focused = index, true
				c.openNavEntry()
				n.Focused = true
			}
		} else {

			delta := 1
			if action == 'k' {
				delta = -1
			}
			n.Top = max(0, min(n.Top+delta, max(0, len(n.Entries)-max(1, c.navRows-2))))
		}
		return
	}
	if action != '\r' {
		c.scroll(terminalui.PaneWheelKey(action))
	}
}

// callerDots marks each file with the callers of its shown captures, once
// more than one caller has changes.
func (c *Pane) callerDots() []string {
	if len(presentation.Callers(&c.view.View)) < 2 {
		return nil
	}
	style := CallerStyle(c.theme)
	dots := make([]string, len(c.view.Files))
	for i, file := range c.view.Files {
		var seen []string
		for _, chunk := range file.Chunks {
			if !c.view.Shows(chunk) || slices.Contains(seen, chunk.Caller) {
				continue
			}
			seen = append(seen, chunk.Caller)
			_, color := style(chunk.Caller)
			dots[i] += color + "●\x1b[0m"
		}
		if dots[i] != "" {
			dots[i] = " " + dots[i]
		}
	}
	return dots
}

// Source: apps/mekugi/internal/router/terminal_scroll.go:7:20@[fc0514ff]
func (c *Pane) scroll(key byte) bool {
	if key == 'r' {
		return false
	}
	total := len(c.lines) + 1
	next, _, ok := terminalui.PaneScroll(key, c.offset, c.rows, total)
	if !ok {
		return false
	}
	c.view.ScrollTo(c.rendering, next)
	c.offset = next
	c.dirty = true
	return true
}
