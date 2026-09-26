package router

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// Navigation is a presentation index. Capture order and evidence stay owned by View.
type liveDiffNavigation struct {
	flat, hidden, focused, filtering bool
	query                            string
	collapsed                        map[string]bool
	entries                          []liveDiffNavEntry
	matches                          []int
	cursor, top                      int
	columns                          int
	savedCursor                      string
	savedTop                         string
	total                            int
	// changes is the Changes tab, shown instead of files when set.
	changes    liveDiffChanges
	changesTab bool
	// dots marks each file row with its callers, by view index.
	dots []string
	// caller is the view's caller filter, for the heading.
	caller string
	// workspace displays rename sources.
	workspace string
}

type liveDiffNavEntry struct {
	id, label, folder  string
	file, depth, count int
}

type liveDiffNavNode struct {
	path    string
	folders map[string]*liveDiffNavNode
	files   []int
	count   int
}

func (n *liveDiffNavigation) width(width int) int {
	if n.hidden || width < 100 {
		return 0
	}
	if n.columns > 0 {
		return min(max(16, n.columns), width-40)
	}
	if n.changesTab {
		// The graph and its attribution need more room than file names.
		return min(46, max(30, width/3))
	}
	return min(34, max(25, width/4))
}

func (n *liveDiffNavigation) rebuild(files []liveDiffFile, workspace string) {
	cursorID, topID := "", ""
	if n.cursor < len(n.entries) {
		cursorID = n.entries[n.cursor].id
	}
	if n.top < len(n.entries) {
		topID = n.entries[n.top].id
	}
	known := make(map[string]bool, len(n.entries))
	for _, entry := range n.entries {
		known[entry.id] = true
	}
	n.entries, n.matches, n.total, n.workspace = nil, nil, 0, workspace
	paths := make(map[int]string)
	for i, file := range files {
		if len(file.Chunks) == 0 {
			continue
		}
		n.total++
		path := pathdisplay.ForWorkspace(workspace, file.Path)
		if !strings.Contains(strings.ToLower(path), strings.ToLower(n.query)) {
			continue
		}
		paths[i] = path
		n.matches = append(n.matches, i)
	}
	slices.SortStableFunc(n.matches, func(a, b int) int {
		if !n.flat && n.query == "" {
			left, right := strings.Split(paths[a], "/"), strings.Split(paths[b], "/")
			for i := 0; i < min(len(left), len(right)); i++ {
				aDir, bDir := i < len(left)-1, i < len(right)-1
				if aDir != bDir {
					if aDir {
						return -1
					}
					return 1
				}
				if order := cmp.Compare(left[i], right[i]); order != 0 {
					return order
				}
			}
		}
		return cmp.Compare(paths[a], paths[b])
	})
	root := &liveDiffNavNode{folders: map[string]*liveDiffNavNode{}}
	for _, i := range n.matches {
		if n.flat || n.query != "" {
			n.entries = append(n.entries, liveDiffNavEntry{id: "f:" + files[i].Key(), label: paths[i], file: i})
			continue
		}
		node := root
		parts := strings.Split(paths[i], "/")
		for j, part := range parts[:len(parts)-1] {
			child := node.folders[part]
			if child == nil {
				folder := strings.Join(parts[:j+1], "/")
				if folder == "" {
					folder = "/"
				}
				child = &liveDiffNavNode{path: folder, folders: map[string]*liveDiffNavNode{}}
				node.folders[part] = child
			}
			child.count++
			node = child
		}
		node.files = append(node.files, i)
	}
	var walk func(*liveDiffNavNode, int)
	walk = func(node *liveDiffNavNode, depth int) {
		names := make([]string, 0, len(node.folders))
		for name := range node.folders {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			child, label := node.folders[name], name
			if label == "" {
				label = "/"
			}
			// Compact chains without flattening branches or losing collapse identity.
			for len(child.folders) == 1 && len(child.files) == 0 {
				for segment, next := range child.folders {
					label = strings.TrimSuffix(label, "/") + "/" + segment
					child = next
				}
			}
			n.entries = append(n.entries, liveDiffNavEntry{id: "d:" + child.path, label: label, folder: child.path, file: -1, depth: depth, count: child.count})
			if !n.collapsed[child.path] {
				walk(child, depth+1)
			}
		}
		for _, i := range node.files {
			_, name, found := strings.CutLast(paths[i], "/")
			if !found {
				name = paths[i]
			}
			n.entries = append(n.entries, liveDiffNavEntry{id: "f:" + files[i].Key(), label: name, file: i, depth: depth})
		}
	}
	if !n.flat && n.query == "" {
		walk(root, 0)
	}
	n.cursor, n.top = min(n.cursor, max(0, len(n.entries)-1)), min(n.top, max(0, len(n.entries)-1))
	for i, entry := range n.entries {
		if entry.id == cursorID {
			n.cursor = i
		}
		if entry.id == topID {
			n.top = i
		}
	}
	// A compacted chain keeps its deepest folder's identity, so when it splits
	// the top row names that folder; show the new ancestors above it too.
	for n.top > 0 && n.top < len(n.entries) {
		above, top := n.entries[n.top-1], n.entries[n.top]
		if above.file >= 0 || above.depth >= top.depth || known[above.id] {
			break
		}
		n.top--
	}
}

func (n *liveDiffNavigation) ensureVisible(rows int) {
	rows = max(1, rows-2)
	n.top = min(n.cursor, max(0, min(n.top, len(n.entries)-rows)))
	if n.cursor >= n.top+rows {
		n.top = n.cursor - rows + 1
	}
}

func (c *liveDiffTerminalController) revealFile() {
	n := &c.navigation
	if len(c.view.Files) == 0 {
		return
	}
	file := c.view.Files[c.view.Selected]
	path := pathdisplay.ForWorkspace(c.workspace, file.Path)
	if !n.flat && n.query == "" {
		for folder := range n.collapsed {
			if strings.HasPrefix(path, strings.TrimSuffix(folder, "/")+"/") {
				delete(n.collapsed, folder)
			}
		}
	}
	n.rebuild(c.files, c.workspace)
	for i, entry := range n.entries {
		if entry.id == "f:"+file.Key() {
			n.cursor = i
			n.ensureVisible(c.navRows)
			// Keep nearby folder context when revealing a file after following a
			// distant update. Selection must not leave its parent just off-screen.
			for parent := i - 1; parent >= 0; parent-- {
				if n.entries[parent].depth < entry.depth {
					if i-parent < max(1, c.navRows-2) {
						n.top = min(n.top, parent)
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
	if len(n.matches) == 0 {
		return
	}
	index := slices.Index(n.matches, c.view.Selected)
	if index < 0 {
		if direction > 0 {
			index = -1
		} else {
			index = 0
		}
	}
	c.view.Selected = n.matches[(index+direction+len(n.matches))%len(n.matches)]
	c.view.Following = false
	c.revealFile()
}

func (c *liveDiffTerminalController) openNavEntry() {
	n := &c.navigation
	if len(n.entries) == 0 {
		return
	}
	entry := n.entries[n.cursor]
	if entry.file < 0 {
		if n.collapsed == nil {
			n.collapsed = make(map[string]bool)
		}
		n.collapsed[entry.folder] = !n.collapsed[entry.folder]
		n.rebuild(c.files, c.workspace)
		n.ensureVisible(c.navRows)
		return
	}
	c.view.Open(entry.file)
	n.focused = false
	n.filtering = false
}

// openChangeRow toggles a change and opens its first file, opens a file row,
// or filters to the caller of a branch row.
func (c *liveDiffTerminalController) openChangeRow() {
	l := &c.navigation.changes
	if l.cursor >= len(l.rows) {
		return
	}
	row := l.rows[l.cursor]
	node := l.nodes[row.node]
	switch row.kind {
	case 'b':
		caller := livediff.CallerKey(node.Caller)
		if c.view.Caller == caller {
			caller = ""
		}
		c.filterCaller(caller)
	case 'c':
		if l.expanded == nil {
			l.expanded = make(map[string]bool)
		}
		l.expanded[node.Change] = !l.expanded[node.Change]
		if len(node.files) > 0 {
			c.openChange(node.Change, node.files[0].file)
		}
		l.rebuild(&c.view, c.workspace)
		l.focusChange(node.Change, c.navRows)
	case 'f':
		c.openChange(node.Change, node.files[row.file].file)
		c.navigation.focused, c.navigation.filtering = false, false
	}
}

// openChange opens one file of a change at its header.
func (c *liveDiffTerminalController) openChange(change string, file int) {
	if file < 0 || file >= len(c.view.Files) {
		return
	}
	c.view.Open(file)
	c.navigation.changes.target = liveDiffChangeTarget{change, c.view.Files[file].Key()}
	c.revealFile()
}

// stepChange opens the next or previous file of a change, in capture order.
func (c *liveDiffTerminalController) stepChange(direction int) {
	l := &c.navigation.changes
	var targets []liveDiffChangeTarget
	var files []int
	for _, node := range l.nodes {
		for _, file := range node.files {
			if file.file >= len(c.view.Files) {
				continue
			}
			targets = append(targets, liveDiffChangeTarget{node.Change, c.view.Files[file.file].Key()})
			files = append(files, file.file)
		}
	}
	if len(targets) == 0 {
		return
	}
	index := slices.Index(targets, l.target)
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
	c.openChange(targets[index].change, files[index])
	l.focusChange(targets[index].change, c.navRows)
}

// filterCaller shows only one caller's changes; an empty caller shows all.
func (c *liveDiffTerminalController) filterCaller(caller string) {
	c.view.FilterCaller(caller)
	c.navigation.changes.target = liveDiffChangeTarget{}
	c.refreshChanges()
}

// refreshChanges rebuilds what the navigator derives from captures: the
// Changes tab, caller marks, and the filter shown in the Files heading.
func (c *liveDiffTerminalController) refreshChanges() {
	c.navigation.changes.rebuild(&c.view, c.workspace)
	c.navigation.dots = c.callerDots()
	c.navigation.caller = c.view.Caller
}

// cycleCaller steps the caller filter through all, then each caller.
func (c *liveDiffTerminalController) cycleCaller() {
	callers := append([]string{""}, liveDiffCallers(&c.view)...)
	next := (slices.Index(callers, c.view.Caller) + 1) % len(callers)
	c.filterCaller(callers[next])
}

func (c *liveDiffTerminalController) editFilter(key byte) {
	n := &c.navigation
	query := &n.query
	if n.changesTab {
		query = &n.changes.query
	}
	switch key {
	case 21:
		*query = ""
	case 13, 10:
		n.filtering = false
		if *query != "" {
			if n.changesTab {
				// Accepting opens the first match's first file without toggling it.
				if first := slices.IndexFunc(n.changes.rows, func(row liveDiffChangeRow) bool { return row.kind == 'c' }); first >= 0 {
					if node := n.changes.nodes[n.changes.rows[first].node]; len(node.files) > 0 {
						c.openChange(node.Change, node.files[0].file)
						n.changes.focusChange(node.Change, c.navRows)
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
	if n.changesTab {
		n.changes.rebuild(&c.view, c.workspace)
		n.changes.cursor, n.changes.top = 0, 0
		return
	}
	n.rebuild(c.files, c.workspace)
	n.cursor, n.top = 0, 0
	if n.query == "" {
		for i, entry := range n.entries {
			if entry.id == n.savedCursor {
				n.cursor = i
			}
			if entry.id == n.savedTop {
				n.top = i
			}
		}
	}
}

func (c *liveDiffTerminalController) navigationKey(key byte) bool {
	n := &c.navigation
	if key == 21 && (n.query != "" && !n.changesTab || n.changes.query != "" && n.changesTab) {
		c.editFilter(key)
		return true
	}
	if n.filtering {
		c.editFilter(key)
		return true
	}
	switch key {
	case '\t':
		n.changesTab = !n.changesTab
		n.hidden, n.filtering = false, false
		if c.lastWidth < 100 && !n.focused {
			n.focused, c.view.Following = true, false
		}
		if n.changesTab {
			n.changes.rebuild(&c.view, c.workspace)
			if target := n.changes.target; target.change != "" {
				n.changes.focusChange(target.change, c.navRows)
			}
		} else if n.focused {
			c.revealFile()
		}
		return true
	case '/':
		if n.query == "" && !n.changesTab {
			if len(n.entries) > 0 {
				n.savedCursor = n.entries[n.cursor].id
				n.savedTop = n.entries[n.top].id
			}
		}
		n.hidden = false
		n.focused, n.filtering, c.view.Following = true, true, false
		return true
	case 't':
		n.flat = !n.flat
		n.rebuild(c.files, c.workspace)
		n.ensureVisible(c.navRows)
		return true
	case 's':
		if c.native && c.lastWidth < 100 {
			// The stacked list stays above the diff: s focuses it, and again hides it.
			n.hidden, n.focused = n.focused, !n.focused
			if n.focused {
				c.view.Following = false
				c.revealFile()
			}
		} else if c.lastWidth < 100 {
			// A narrow viewport has no dock; the same key toggles its picker.
			n.hidden = false
			n.focused = !n.focused
			if n.focused {
				c.view.Following = false
				c.revealFile()
			}
		} else {
			n.hidden = !n.hidden
			n.focused = false
		}
		n.filtering = false
		return true
	}
	if !n.focused {
		return false
	}
	if n.changesTab {
		return c.changesKey(key)
	}
	switch key {
	case 'j':
		n.cursor = min(n.cursor+1, max(0, len(n.entries)-1))
	case 'k':
		n.cursor = max(0, n.cursor-1)
	case ' ':
		n.cursor = min(n.cursor+max(1, c.navRows-2), max(0, len(n.entries)-1))
	case 'b':
		n.cursor = max(0, n.cursor-max(1, c.navRows-2))
	case 'g':
		n.cursor = 0
	case 'G':
		n.cursor = max(0, len(n.entries)-1)
	case 13, 10:
		c.openNavEntry()
	case 'h':
		if len(n.entries) > 0 {
			entry := n.entries[n.cursor]
			if entry.file < 0 && !n.collapsed[entry.folder] {
				c.openNavEntry()
			} else {
				for i := n.cursor - 1; i >= 0; i-- {
					if n.entries[i].depth < entry.depth {
						n.cursor = i
						break
					}
				}
			}
		}
	case 'l':
		if len(n.entries) > 0 {
			entry := n.entries[n.cursor]
			if entry.file < 0 && n.collapsed[entry.folder] {
				c.openNavEntry()
			} else {
				n.cursor = min(n.cursor+1, len(n.entries)-1)
			}
		}
	default:
		return false
	}
	n.ensureVisible(c.navRows)
	return true
}

func (n *liveDiffNavigation) render(files []liveDiffFile, counts []livediff.Counts, selected, width, rows int, theme livediff.Theme) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	mode := "tree"
	if n.flat || n.query != "" {
		mode = "flat"
	}
	heading := fmt.Sprintf(" Files  %d/%d · %s", len(n.matches), n.total, mode)
	if n.caller != "" {
		name, _ := liveDiffCallerStyle(theme)(n.caller)
		heading += " · @" + livediff.Safe(name, false)
	}
	if n.focused {
		heading = theme.Accent() + "▎" + strings.TrimPrefix(heading, " ") + "\x1b[0m"
	}
	out[0] = ansi.Truncate(heading, max(0, width-1), "…")
	if rows < 2 {
		return out
	}
	filter := " / filter paths"
	if n.query != "" || n.filtering {
		filter = " / " + livediff.Safe(n.query, false)
		if n.filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate("\x1b[2m"+filter+"\x1b[0m", max(0, width-1), "…")
	// Rows never stay scrolled off the top while space remains below.
	n.top = min(n.top, max(0, len(n.entries)-(rows-2)))
	scrollbar := len(n.entries) > rows-2 && rows > 2
	contentWidth := max(0, width-1)
	if scrollbar {
		contentWidth = max(0, contentWidth-1)
	}
	for row := 2; row < rows; row++ {
		index := n.top + row - 2
		if index >= len(n.entries) {
			if row == 2 {
				out[row] = " No matching files"
			}
			break
		}
		entry := n.entries[index]
		indent := strings.Repeat("  ", min(entry.depth, 4))
		label, stats := "", ""
		if entry.file < 0 {
			arrow := "▼"
			if n.collapsed[entry.folder] {
				arrow = "▶"
			}
			label = indent + theme.Accent() + arrow + "\x1b[39m " + livediff.Safe(entry.label, false)
			stats = fmt.Sprintf(" \x1b[2m(%d)\x1b[22m", entry.count)
		} else {
			var regions []mekugi.ReviewFile
			for _, chunk := range files[entry.file].Chunks {
				regions = append(regions, chunk.Review)
			}
			label = indent + livediff.Safe(entry.label, false)
			if len(regions) > 0 {
				label = indent + liveDiffFileLabel(liveDiffStatusOf(regions...), entry.label, n.workspace, theme)
			}
			if entry.file < len(counts) {
				stats = liveDiffCountStats(counts[entry.file], theme)
			}
			if entry.file < len(n.dots) {
				stats = n.dots[entry.file] + stats
			}
		}
		available := max(0, contentWidth-ansi.StringWidth(stats))
		label = ansi.Truncate(label, available, "…")
		line := ansi.Truncate(label+stats, contentWidth, "")
		if (n.focused && index == n.cursor) || (!n.focused && entry.file == selected && entry.file >= 0) {
			line = liveDiffSelectRow(line, contentWidth, theme)
		}
		out[row] = line
	}
	if scrollbar {
		track := rows - 2
		thumb := min(track-1, n.top*track/max(1, len(n.entries)-track))
		for row := 2; row < rows; row++ {
			line := ansi.Truncate(out[row], max(0, width-2), "")
			bar := "│"
			if row-2 == thumb {
				bar = "┃"
			}
			out[row] = line + strings.Repeat(" ", max(0, width-2-ansi.StringWidth(line))) + "\x1b[2m" + bar + "\x1b[0m"
		}
	}
	return out
}

// liveDiffSelectRow marks the active row without reserving a marker column.
// Inline style resets restore the fill, and the row fills its available width.
func liveDiffSelectRow(line string, width int, theme livediff.Theme) string {
	fill := theme.SelectionBackground()
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+fill)
	return fill + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
}

// liveDiffStatus is a file row's net change, coded like git's short status:
// A, D, M, R (rename only), RM (rename with edits), or UU (the net diff still
// adds conflict markers that mchanges revert or apply left for resolution).
type liveDiffStatus struct {
	before, after string
	// edited reports content changes, or content that could not be captured.
	edited, conflict bool
}

// liveDiffStatusOf classifies a file's diff regions, in capture order.
func liveDiffStatusOf(regions ...mekugi.ReviewFile) liveDiffStatus {
	var status liveDiffStatus
	if len(regions) > 0 {
		status.before = regions[0].BeforePath
	}
	for _, region := range regions {
		status.add(region)
	}
	return status
}

// add folds a later capture of the file into its status; before stays the
// first capture's.
func (s *liveDiffStatus) add(region mekugi.ReviewFile) {
	s.after = region.AfterPath
	added, removed := region.LineCounts()
	// Unknown content is not evidence of a pure rename.
	s.edited = s.edited || added != 0 || removed != 0
	if region.Binary {
		if _, sides, found := strings.Cut(region.Diff, " differ ("); found {
			before, after, _ := strings.Cut(strings.TrimSuffix(strings.TrimSpace(sides), ")"), " -> ")
			s.edited = s.edited || before != after
		}
	}
	s.conflict = s.conflict || strings.Contains(region.Diff, "\n+>>>>>>> mchanges ")
}

func (s liveDiffStatus) shortCode() string {
	switch {
	case s.conflict:
		return "UU"
	case s.before == "":
		return "A"
	case s.after == "":
		return "D"
	case s.before != s.after && s.edited:
		return "RM"
	case s.before != s.after:
		return "R"
	}
	return "M"
}

func (s liveDiffStatus) code(theme livediff.Theme) (string, string) {
	code := s.shortCode()
	switch code {
	case "UU", "D":
		return code, theme.Foreground(chroma.GenericDeleted)
	case "A":
		return code, theme.Foreground(chroma.GenericInserted)
	case "RM", "R":
		return code, theme.Accent()
	}
	return code, theme.Foreground(chroma.LiteralNumberInteger)
}

// liveDiffFileLabel prefixes a file label with its colored status; a rename
// also names its source. Every file row (tree, flat list, Changes tab,
// streaming title) uses this format.
func liveDiffFileLabel(status liveDiffStatus, label, workspace string, theme livediff.Theme) string {
	code, color := status.code(theme)
	label = livediff.Safe(label, false)
	if status.before != "" && status.after != "" && status.before != status.after {
		// The source's base name when it stayed in the same folder.
		from := pathdisplay.ForWorkspace(workspace, status.before)
		if path.Dir(status.before) == path.Dir(status.after) {
			from = path.Base(status.before)
		}
		label = livediff.Safe(from, false) + " → " + label
	}
	return color + code + "\x1b[39m " + label
}

// liveDiffCountStats shows known line counts; unknown counts are not zero.
func liveDiffCountStats(count livediff.Counts, theme livediff.Theme) string {
	if count.Added < 0 || count.Removed < 0 {
		return " \x1b[2m?\x1b[22m"
	}
	return fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), count.Added, theme.Foreground(chroma.GenericDeleted), count.Removed)
}

// changesKey moves through the Changes tab while it has focus.
func (c *liveDiffTerminalController) changesKey(key byte) bool {
	l := &c.navigation.changes
	last := max(0, len(l.rows)-1)
	switch key {
	case 'j':
		l.cursor = min(l.cursor+1, last)
	case 'k':
		l.cursor = max(0, l.cursor-1)
	case ' ':
		l.cursor = min(l.cursor+max(1, c.navRows-2), last)
	case 'b':
		l.cursor = max(0, l.cursor-max(1, c.navRows-2))
	case 'g':
		l.cursor = 0
	case 'G':
		l.cursor = last
	case 13, 10:
		c.openChangeRow()
	case 'h', 'l':
		// l expands a change, h collapses it; from a file row, h returns to its change.
		if l.cursor >= len(l.rows) || l.rows[l.cursor].kind == 'b' {
			break
		}
		row := l.rows[l.cursor]
		change := l.nodes[row.node].Change
		switch {
		case key == 'h' && row.kind == 'f':
			l.focusChange(change, c.navRows)
		case row.kind == 'f' || l.expanded[change] == (key == 'l'):
			if key == 'l' {
				l.cursor = min(l.cursor+1, last)
			}
		default:
			if l.expanded == nil {
				l.expanded = make(map[string]bool)
			}
			l.expanded[change] = key == 'l'
			l.rebuild(&c.view, c.workspace)
			l.focusChange(change, c.navRows)
		}
	default:
		return false
	}
	l.ensureVisible(c.navRows)
	return true
}
