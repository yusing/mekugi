package router

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func (c *liveDiffTerminalController) revealFile() {
	n := &c.navigation
	if len(c.view.Files) == 0 {
		return
	}
	file := c.view.Files[c.view.Selected]
	path := pathdisplay.ForWorkspace(c.workspace, file.Path)
	if !n.Flat && n.Query == "" {
		for folder := range n.Collapsed {
			if strings.HasPrefix(path, strings.TrimSuffix(folder, "/")+"/") {
				delete(n.Collapsed, folder)
			}
		}
	}
	n.Rebuild(c.files, c.workspace)
	for i, entry := range n.Entries {
		if entry.ID == "f:"+file.Key() {
			n.Cursor = i
			n.EnsureVisible(c.navRows)
			// Keep nearby folder context when revealing a file after following a
			// distant update. Selection must not leave its parent just off-screen.
			for parent := i - 1; parent >= 0; parent-- {
				if n.Entries[parent].Depth < entry.Depth {
					if i-parent < max(1, c.navRows-2) {
						n.Top = min(n.Top, parent)
					}
					break
				}
			}
			break
		}
	}
}

func (c *liveDiffTerminalController) stepFile(direction int) {
	n := &c.navigation
	if len(n.Matches) == 0 {
		return
	}
	index := slices.Index(n.Matches, c.view.Selected)
	if index < 0 {
		if direction > 0 {
			index = -1
		} else {
			index = 0
		}
	}
	c.view.Selected = n.Matches[(index+direction+len(n.Matches))%len(n.Matches)]
	c.view.Following = false
	c.revealFile()
}

func (c *liveDiffTerminalController) openNavEntry() {
	n := &c.navigation
	if len(n.Entries) == 0 {
		return
	}
	entry := n.Entries[n.Cursor]
	if entry.File < 0 {
		if n.Collapsed == nil {
			n.Collapsed = make(map[string]bool)
		}
		n.Collapsed[entry.Folder] = !n.Collapsed[entry.Folder]
		n.Rebuild(c.files, c.workspace)
		n.EnsureVisible(c.navRows)
		return
	}
	c.view.Open(entry.File)
	n.Focused = false
	n.Filtering = false
	c.back = liveDiffBack{kind: 'f'}
}

// openChangeRow toggles a change and opens its first file, opens a file row,
// or filters to the caller of a branch row.
func (c *liveDiffTerminalController) openChangeRow() {
	l := &c.navigation.Changes
	if l.Cursor >= len(l.Rows) {
		return
	}
	row := l.Rows[l.Cursor]
	node := l.Nodes[row.Node]
	switch {
	case row.Kind == 'b':
		previous, caller := c.view.Caller, livediff.CallerKey(node.Caller)
		if previous == caller {
			caller = ""
		}
		c.filterCaller(caller)
		c.back = liveDiffBack{kind: 'b', caller: previous}
	case row.Kind == 'c' && len(node.Files) == 1, row.Kind == 'f':
		c.openChange(node.Change, node.Files[row.File].File)
		c.navigation.Focused, c.navigation.Filtering = false, false
		c.back = liveDiffBack{kind: 'f'}
	case row.Kind == 'c':
		if l.Expanded == nil {
			l.Expanded = make(map[string]bool)
		}
		l.Expanded[node.Change] = !l.Expanded[node.Change]
		if len(node.Files) > 0 {
			c.openChange(node.Change, node.Files[0].File)
		}
		l.Rebuild(&c.view, c.workspace)
		l.FocusChange(node.Change, c.navRows)
	}
}

// previewChangeRow shows the file under the Changes cursor while the list
// keeps focus; a change shows its first file.
func (c *liveDiffTerminalController) previewChangeRow() {
	l := &c.navigation.Changes
	if l.Cursor >= len(l.Rows) {
		return
	}
	row := l.Rows[l.Cursor]
	node := l.Nodes[row.Node]
	switch {
	case row.Kind == 'f':
		c.openChange(node.Change, node.Files[row.File].File)
	case row.Kind == 'c' && len(node.Files) > 0:
		c.openChange(node.Change, node.Files[0].File)
	}
}

// openChange opens one file of a change at its header.
func (c *liveDiffTerminalController) openChange(change string, file int) {
	if file < 0 || file >= len(c.view.Files) {
		return
	}
	c.view.Open(file)
	c.navigation.Changes.Target = diffview.ChangeTarget{Change: change, File: c.view.Files[file].Key()}
	c.revealFile()
}

// stepChange opens the next or previous file of a change, in capture order.
func (c *liveDiffTerminalController) stepChange(direction int) {
	l := &c.navigation.Changes
	var targets []diffview.ChangeTarget
	var files []int
	for _, node := range l.Nodes {
		for _, file := range node.Files {
			if file.File >= len(c.view.Files) {
				continue
			}
			targets = append(targets, diffview.ChangeTarget{Change: node.Change, File: c.view.Files[file.File].Key()})
			files = append(files, file.File)
		}
	}
	if len(targets) == 0 {
		return
	}
	index := slices.Index(targets, l.Target)
	if index < 0 {
		// Start from the open file's first change, before or after it.
		index = slices.IndexFunc(files, func(file int) bool { return file == c.view.Selected })
		if index < 0 && direction > 0 {
			index = -1
		} else if index < 0 {
			index = 0
		} else if direction > 0 {
			index--
		} else {
			index++
		}
	}
	index = (index + direction + len(targets)) % len(targets)
	c.openChange(targets[index].Change, files[index])
	l.FocusChange(targets[index].Change, c.navRows)
}

// filterCaller shows only one caller's changes; an empty caller shows all.
func (c *liveDiffTerminalController) filterCaller(caller string) {
	c.view.FilterCaller(caller)
	c.navigation.Changes.Target = diffview.ChangeTarget{}
	c.refreshChanges()
}

// refreshChanges rebuilds what the navigator derives from captures: the
// Changes tab, caller marks, and the filter shown in the Files heading.
func (c *liveDiffTerminalController) refreshChanges() {
	c.files = make([]livediff.File, len(c.view.Files))
	for i, file := range c.view.Files {
		c.files[i] = c.view.Visible[file.Key()]
	}
	c.navigation.Rebuild(c.files, c.workspace)
	c.navigation.Changes.Scope = c.navigation.Scope
	c.navigation.Changes.Rebuild(&c.view, c.workspace)
	c.navigation.Dots = c.callerDots()
	c.navigation.Caller = c.view.Caller
}

// cycleCaller steps the caller filter through all, then each caller.
func (c *liveDiffTerminalController) cycleCaller() {
	callers := append([]string{""}, diffview.Callers(&c.view)...)
	next := (slices.Index(callers, c.view.Caller) + 1) % len(callers)
	c.filterCaller(callers[next])
}

func (c *liveDiffTerminalController) editFilter(key byte) {
	n := &c.navigation
	query := &n.Query
	if n.ChangesTab {
		query = &n.Changes.Query
	}
	switch key {
	case 21:
		*query = ""
	case 13, 10:
		n.Filtering = false
		if *query != "" {
			if n.ChangesTab {
				// Accepting opens the first match's first file without toggling it.
				if first := slices.IndexFunc(n.Changes.Rows, func(row diffview.ChangeRow) bool { return row.Kind == 'c' }); first >= 0 {
					if node := n.Changes.Nodes[n.Changes.Rows[first].Node]; len(node.Files) > 0 {
						c.openChange(node.Change, node.Files[0].File)
						n.Changes.FocusChange(node.Change, c.navRows)
					}
				}
			} else {
				c.openNavEntry()
			}
		}
		return
	case 127, 8:
		_, size := utf8.DecodeLastRuneInString(*query)
		*query = (*query)[:len(*query)-size]
	default:
		if key >= 32 && len(*query) < 4096 {
			*query += string([]byte{key})
		}
	}
	if n.ChangesTab {
		n.Changes.Rebuild(&c.view, c.workspace)
		n.Changes.Cursor, n.Changes.Top = 0, 0
		return
	}
	n.Rebuild(c.files, c.workspace)
	n.Cursor, n.Top = 0, 0
	if n.Query == "" {
		for i, entry := range n.Entries {
			if entry.ID == n.SavedCursor {
				n.Cursor = i
			}
			if entry.ID == n.SavedTop {
				n.Top = i
			}
		}
	}
}

func (c *liveDiffTerminalController) navigationKey(key byte) bool {
	n := &c.navigation
	if key == 21 && (n.Query != "" && !n.ChangesTab || n.Changes.Query != "" && n.ChangesTab) {
		c.editFilter(key)
		return true
	}
	if n.Filtering {
		c.editFilter(key)
		return true
	}
	switch key {
	case '\t':
		n.ChangesTab = !n.ChangesTab
		n.Hidden, n.Filtering = false, false
		if c.lastWidth < 100 && !n.Focused {
			n.Focused, c.view.Following = true, false
		}
		if n.ChangesTab {
			n.Changes.Rebuild(&c.view, c.workspace)
			if target := n.Changes.Target; target.Change != "" {
				n.Changes.FocusChange(target.Change, c.navRows)
			}
		} else if n.Focused {
			c.revealFile()
		}
		return true
	case '/':
		if n.Query == "" && !n.ChangesTab {
			if len(n.Entries) > 0 {
				n.SavedCursor = n.Entries[n.Cursor].ID
				n.SavedTop = n.Entries[n.Top].ID
			}
		}
		n.Hidden = false
		n.Focused, n.Filtering, c.view.Following = true, true, false
		return true
	case 't':
		n.Flat = !n.Flat
		n.Rebuild(c.files, c.workspace)
		n.EnsureVisible(c.navRows)
		return true
	case 's':
		// Focus the navigator first; a second press hides it.
		n.Hidden, n.Focused = n.Focused, !n.Focused
		n.Filtering = false
		if n.Focused {
			c.view.Following = false
			c.revealFile()
		}
		return true
	}
	if !n.Focused {
		return false
	}
	if n.ChangesTab {
		return c.changesKey(key)
	}
	cursor := n.Cursor
	defer func() {
		// Moving onto a file shows it while the list keeps focus.
		if n.Cursor != cursor && n.Cursor < len(n.Entries) && n.Entries[n.Cursor].File >= 0 {
			c.view.Open(n.Entries[n.Cursor].File)
		}
	}()
	switch key {
	case 'j':
		n.Cursor = min(n.Cursor+1, max(0, len(n.Entries)-1))
	case 'k':
		n.Cursor = max(0, n.Cursor-1)
	case ' ':
		n.Cursor = min(n.Cursor+max(1, c.navRows-2), max(0, len(n.Entries)-1))
	case 'b':
		n.Cursor = max(0, n.Cursor-max(1, c.navRows-2))
	case 'g':
		n.Cursor = 0
	case 'G':
		n.Cursor = max(0, len(n.Entries)-1)
	case 13, 10:
		c.openNavEntry()
	case 'h':
		if len(n.Entries) > 0 {
			entry := n.Entries[n.Cursor]
			if entry.File < 0 && !n.Collapsed[entry.Folder] {
				c.openNavEntry()
			} else {
				for i := n.Cursor - 1; i >= 0; i-- {
					if n.Entries[i].Depth < entry.Depth {
						n.Cursor = i
						break
					}
				}
			}
		}
	case 'l':
		if len(n.Entries) > 0 {
			entry := n.Entries[n.Cursor]
			if entry.File < 0 && n.Collapsed[entry.Folder] {
				c.openNavEntry()
			} else {
				n.Cursor = min(n.Cursor+1, len(n.Entries)-1)
			}
		}
	default:
		return false
	}
	n.EnsureVisible(c.navRows)
	return true
}

// changesKey moves through the Changes tab while it has focus.
func (c *liveDiffTerminalController) changesKey(key byte) bool {
	l := &c.navigation.Changes
	last := max(0, len(l.Rows)-1)
	cursor := l.Cursor
	defer func() {
		if l.Cursor != cursor {
			c.previewChangeRow()
		}
	}()
	switch key {
	case 'j':
		l.Cursor = min(l.Cursor+1, last)
	case 'k':
		l.Cursor = max(0, l.Cursor-1)
	case ' ':
		l.Cursor = min(l.Cursor+max(1, c.navRows-2), last)
	case 'b':
		l.Cursor = max(0, l.Cursor-max(1, c.navRows-2))
	case 'g':
		l.Cursor = 0
	case 'G':
		l.Cursor = last
	case 13, 10:
		c.openChangeRow()
	case 'h', 'l':
		// l expands a change, h collapses it; from a file row, h returns to its
		// change. A single-file change stays as it is, nested or inline.
		if l.Cursor >= len(l.Rows) || l.Rows[l.Cursor].Kind == 'b' {
			break
		}
		row := l.Rows[l.Cursor]
		change, single := l.Nodes[row.Node].Change, len(l.Nodes[row.Node].Files) == 1
		switch {
		case single && l.Inline:
		case key == 'h' && row.Kind == 'f':
			l.FocusChange(change, c.navRows)
		case row.Kind == 'f' || single || l.Expanded[change] == (key == 'l'):
			if key == 'l' {
				l.Cursor = min(l.Cursor+1, last)
			}
		default:
			if l.Expanded == nil {
				l.Expanded = make(map[string]bool)
			}
			l.Expanded[change] = key == 'l'
			l.Rebuild(&c.view, c.workspace)
			l.FocusChange(change, c.navRows)
		}
	default:
		return false
	}
	l.EnsureVisible(c.navRows)
	return true
}
