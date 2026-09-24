package router

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
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
	savedCursor                      string
	savedTop                         string
	total                            int
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
	n.entries, n.matches, n.total = nil, nil, 0
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
			n.ensureVisible(c.rows)
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
		n.ensureVisible(c.rows)
		return
	}
	c.view.Selected, c.view.Following = entry.file, false
	n.focused = false
	n.filtering = false
}

func (c *liveDiffTerminalController) editFilter(key byte) {
	n := &c.navigation
	switch key {
	case 21:
		n.query = ""
	case 13, 10:
		n.filtering = false
		if n.query != "" {
			c.openNavEntry()
		}
		return
	case 127, 8:
		_, size := utf8.DecodeLastRuneInString(n.query)
		n.query = n.query[:len(n.query)-size]
	default:
		if key >= 32 && len(n.query) < 4096 {
			n.query += string([]byte{key})
		}
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
	if key == 21 && n.query != "" {
		c.editFilter(key)
		return true
	}
	if n.filtering {
		c.editFilter(key)
		return true
	}
	switch key {
	case '/':
		if n.query == "" {
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
		n.ensureVisible(c.rows)
		return true
	case 's':
		if c.lastWidth < 100 {
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
	switch key {
	case 'j':
		n.cursor = min(n.cursor+1, max(0, len(n.entries)-1))
	case 'k':
		n.cursor = max(0, n.cursor-1)
	case ' ':
		n.cursor = min(n.cursor+max(1, c.rows-2), max(0, len(n.entries)-1))
	case 'b':
		n.cursor = max(0, n.cursor-max(1, c.rows-2))
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
	n.ensureVisible(c.rows)
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
		marker := "  "
		if entry.file == selected && entry.file >= 0 {
			marker = "▎ "
		}
		if n.focused && index == n.cursor {
			marker = "> "
		}
		indent := strings.Repeat("  ", min(entry.depth, 4))
		label, stats := "", ""
		if entry.file < 0 {
			arrow := "▼"
			if n.collapsed[entry.folder] {
				arrow = "▶"
			}
			label = indent + theme.Accent() + arrow + "\x1b[39m  " + livediff.Safe(entry.label, false)
			stats = fmt.Sprintf(" \x1b[2m(%d)\x1b[22m", entry.count)
		} else {
			file := files[entry.file]
			status, color := "M", theme.Foreground(chroma.LiteralNumberInteger)
			if len(file.Chunks) > 0 {
				first, last := file.Chunks[0].Review, file.Chunks[len(file.Chunks)-1].Review
				switch {
				case first.BeforePath == "":
					status, color = "A", theme.Foreground(chroma.GenericInserted)
				case last.AfterPath == "":
					status, color = "D", theme.Foreground(chroma.GenericDeleted)
				case first.BeforePath != last.AfterPath:
					status, color = "R", theme.Accent()
				}
			}
			label = indent + color + status + "\x1b[39m  " + livediff.Safe(entry.label, false)
			if entry.file < len(counts) {
				count := counts[entry.file]
				if count.Added < 0 || count.Removed < 0 {
					stats = " \x1b[2m?\x1b[22m"
				} else {
					stats = fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), count.Added, theme.Foreground(chroma.GenericDeleted), count.Removed)
				}
			}
		}
		available := max(0, contentWidth-2-ansi.StringWidth(stats))
		label = ansi.Truncate(label, available, "…")
		line := marker + label + stats
		if marker != "  " {
			line = theme.Accent() + marker + "\x1b[39m" + label + stats
		}
		out[row] = ansi.Truncate(line, contentWidth, "")
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
