package diffview

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// Navigation is a presentation index. Capture order and evidence stay owned by View.
type Navigation struct {
	Flat, Hidden, Focused, Filtering bool
	Query                            string
	Collapsed                        map[string]bool
	Entries                          []NavEntry
	Matches                          []int
	Cursor, Top                      int
	// hover is the pointed entry plus one; zero points at none.
	Hover       int
	Columns     int
	SavedCursor string
	SavedTop    string
	total       int
	// changes is the Changes tab, shown instead of files when set.
	Changes    liveDiffChanges
	ChangesTab bool
	// dots marks each file row with its callers, by view index.
	Dots []string
	// caller is the view's caller filter, for the heading.
	Caller string
	// Scope names the selected journal task range, when one is available.
	Scope string
	// workspace displays rename sources.
	workspace string
}

type NavEntry struct {
	ID, Label, Folder  string
	File, Depth, Count int
}

type liveDiffNavNode struct {
	path    string
	folders map[string]*liveDiffNavNode
	files   []int
	count   int
}

func (n *Navigation) Width(width int) int {
	if n.Hidden || width < 100 {
		return 0
	}
	if n.Columns > 0 {
		return min(max(16, n.Columns), width-40)
	}
	if n.ChangesTab {
		// The graph and its attribution need more room than file names.
		return min(46, max(30, width/3))
	}
	return min(34, max(25, width/4))
}

func (n *Navigation) Rebuild(files []livediff.File, workspace string) {
	cursorID, topID := "", ""
	if n.Cursor < len(n.Entries) {
		cursorID = n.Entries[n.Cursor].ID
	}
	if n.Top < len(n.Entries) {
		topID = n.Entries[n.Top].ID
	}
	known := make(map[string]bool, len(n.Entries))
	for _, entry := range n.Entries {
		known[entry.ID] = true
	}
	n.Entries, n.Matches, n.total, n.workspace = nil, nil, 0, workspace
	paths := make(map[int]string)
	for i, file := range files {
		if len(file.Chunks) == 0 {
			continue
		}
		n.total++
		path := pathdisplay.ForWorkspace(workspace, file.Path)
		if !strings.Contains(strings.ToLower(path), strings.ToLower(n.Query)) {
			continue
		}
		paths[i] = path
		n.Matches = append(n.Matches, i)
	}
	slices.SortStableFunc(n.Matches, func(a, b int) int {
		if !n.Flat && n.Query == "" {
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
	for _, i := range n.Matches {
		if n.Flat || n.Query != "" {
			n.Entries = append(n.Entries, NavEntry{ID: "f:" + files[i].Key(), Label: paths[i], File: i})
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
			n.Entries = append(n.Entries, NavEntry{ID: "d:" + child.path, Label: label, Folder: child.path, File: -1, Depth: depth, Count: child.count})
			if !n.Collapsed[child.path] {
				walk(child, depth+1)
			}
		}
		for _, i := range node.files {
			_, name, found := strings.CutLast(paths[i], "/")
			if !found {
				name = paths[i]
			}
			n.Entries = append(n.Entries, NavEntry{ID: "f:" + files[i].Key(), Label: name, File: i, Depth: depth})
		}
	}
	if !n.Flat && n.Query == "" {
		walk(root, 0)
	}
	n.Cursor, n.Top = min(n.Cursor, max(0, len(n.Entries)-1)), min(n.Top, max(0, len(n.Entries)-1))
	for i, entry := range n.Entries {
		if entry.ID == cursorID {
			n.Cursor = i
		}
		if entry.ID == topID {
			n.Top = i
		}
	}
	// A compacted chain keeps its deepest folder's identity, so when it splits
	// the top row names that folder; show the new ancestors above it too.
	for n.Top > 0 && n.Top < len(n.Entries) {
		above, top := n.Entries[n.Top-1], n.Entries[n.Top]
		if above.File >= 0 || above.Depth >= top.Depth || known[above.ID] {
			break
		}
		n.Top--
	}
}

func (n *Navigation) EnsureVisible(rows int) {
	rows = max(1, rows-2)
	n.Top = min(n.Cursor, max(0, min(n.Top, len(n.Entries)-rows)))
	if n.Cursor >= n.Top+rows {
		n.Top = n.Cursor - rows + 1
	}
}

func (n *Navigation) Render(files []livediff.File, counts []livediff.Counts, selected, width, rows int, theme livediff.Theme) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	mode := "tree"
	if n.Flat || n.Query != "" {
		mode = "flat"
	}
	heading := fmt.Sprintf(" Files  %d/%d · %s", len(n.Matches), n.total, mode)
	if n.Scope != "" {
		heading += " · " + livediff.Safe(n.Scope, false)
	}
	if n.Caller != "" {
		name, _ := CallerStyle(theme)(n.Caller)
		heading += " · @" + livediff.Safe(name, false)
	}
	if n.Focused {
		heading = theme.Accent() + "▎" + strings.TrimPrefix(heading, " ") + "\x1b[0m"
	}
	out[0] = ansi.Truncate(heading, max(0, width-1), "…")
	if rows < 2 {
		return out
	}
	filter := " / filter paths"
	if n.Query != "" || n.Filtering {
		filter = " / " + livediff.Safe(n.Query, false)
		if n.Filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate(livediff.Subtle+filter+"\x1b[0m", max(0, width-1), "…")
	// Rows never stay scrolled off the top while space remains below.
	n.Top = min(n.Top, max(0, len(n.Entries)-(rows-2)))
	scrollbar := len(n.Entries) > rows-2 && rows > 2
	contentWidth := max(0, width-1)
	if scrollbar {
		contentWidth = max(0, contentWidth-1)
	}
	for row := 2; row < rows; row++ {
		index := n.Top + row - 2
		if index >= len(n.Entries) {
			if row == 2 {
				out[row] = " No matching files"
			}
			break
		}
		entry := n.Entries[index]
		indent := strings.Repeat("  ", min(entry.Depth, 4))
		label, stats := "", ""
		if entry.File < 0 {
			arrow := "▼"
			if n.Collapsed[entry.Folder] {
				arrow = "▶"
			}
			label = indent + theme.Accent() + arrow + "\x1b[39m " + livediff.Safe(entry.Label, false)
			stats = fmt.Sprintf(" "+livediff.Subtle+"(%d)"+livediff.SubtleReset, entry.Count)
		} else {
			var regions []mekugi.ReviewFile
			for _, chunk := range files[entry.File].Chunks {
				regions = append(regions, chunk.Review)
			}
			label = indent + livediff.Safe(entry.Label, false)
			if len(regions) > 0 {
				label = indent + FileLabel(StatusOf(regions...), entry.Label, n.workspace, theme)
			}
			if entry.File < len(counts) && !StatusOf(regions...).Directory {
				stats = CountStats(counts[entry.File], theme)
			}
			if entry.File < len(n.Dots) {
				stats = n.Dots[entry.File] + stats
			}
		}
		available := max(0, contentWidth-ansi.StringWidth(stats))
		label = ansi.Truncate(label, available, "…")
		line := ansi.Truncate(label+stats, contentWidth, "")
		if (n.Focused && index == n.Cursor) || (!n.Focused && entry.File == selected && entry.File >= 0) {
			line = liveDiffSelectRow(line, contentWidth, theme)
		} else if index == n.Hover-1 {
			line = indent + liveDiffHoverRow(strings.TrimPrefix(line, indent))
		}
		out[row] = line
	}
	if scrollbar {
		track := rows - 2
		thumb := min(track-1, n.Top*track/max(1, len(n.Entries)-track))
		for row := 2; row < rows; row++ {
			line := ansi.Truncate(out[row], max(0, width-2), "")
			bar := "│"
			if row-2 == thumb {
				bar = "┃"
			}
			out[row] = line + strings.Repeat(" ", max(0, width-2-ansi.StringWidth(line))) + livediff.Subtle + bar + "\x1b[0m"
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

// liveDiffHoverRow underlines the row under the pointer.
func liveDiffHoverRow(line string) string {
	return "\x1b[4m" + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m\x1b[4m") + "\x1b[24m"
}

// Status is a file row's net change, coded like git's short status:
// ? (incomplete evidence), A, D, M, R (rename only), RM (rename with edits), or UU (the net diff still
// adds conflict markers that mchanges revert or apply left for resolution).
type Status struct {
	Before, After string
	// Edited reports captured content changes, not missing evidence.
	Edited, Conflict bool
	Incomplete       bool
	Directory        bool
}

// StatusOf classifies a file's diff regions, in capture order.
func StatusOf(regions ...mekugi.ReviewFile) Status {
	var status Status
	if len(regions) > 0 {
		status.Before = regions[0].BeforePath
	}
	for _, region := range regions {
		status.Add(region)
	}
	return status
}

// add folds a later capture of the file into its status; before stays the
// first capture's.
func (s *Status) Add(region mekugi.ReviewFile) {
	s.After = region.AfterPath
	s.Directory = region.Directory
	s.Incomplete = s.Incomplete || region.Incomplete != "" && !region.Directory
	added, removed := region.LineCounts()
	s.Edited = s.Edited || added > 0 || removed > 0 || region.Directory
	if region.Binary {
		if _, sides, found := strings.Cut(region.Diff, " differ ("); found {
			before, after, _ := strings.Cut(strings.TrimSuffix(strings.TrimSpace(sides), ")"), " -> ")
			s.Edited = s.Edited || before != after
		}
	}
	s.Conflict = s.Conflict || strings.Contains(region.Diff, "\n+>>>>>>> mchanges ")
}

func (s Status) ShortCode() string {
	switch {
	case s.Incomplete:
		return "?"
	case s.Conflict:
		return "UU"
	case s.Before == "":
		return "A"
	case s.After == "":
		return "D"
	case s.Before != s.After && s.Edited:
		return "RM"
	case s.Before != s.After:
		return "R"
	}
	return "M"
}

func (s Status) code(theme livediff.Theme) (string, string) {
	code := s.ShortCode()
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

// FileLabel prefixes a file label with its colored status; a rename
// also names its source. Every file row (tree, flat list, Changes tab,
// streaming title) uses this format.
func FileLabel(status Status, label, workspace string, theme livediff.Theme) string {
	if status.Directory {
		label = strings.TrimSuffix(label, "/") + "/"
	}
	code, color := status.code(theme)
	label = livediff.Safe(label, false)
	if status.Before != "" && status.After != "" && status.Before != status.After {
		// The source's base name when it stayed in the same folder.
		from := pathdisplay.ForWorkspace(workspace, status.Before)
		if path.Dir(status.Before) == path.Dir(status.After) {
			from = path.Base(status.Before)
		}
		label = livediff.Safe(from, false) + " → " + label
	}
	return color + code + "\x1b[39m " + label
}

// CountStats shows known line counts; unknown counts are not zero.
func CountStats(count livediff.Counts, theme livediff.Theme) string {
	if count.Added < 0 || count.Removed < 0 {
		return " " + livediff.Subtle + "?" + livediff.SubtleReset
	}
	return fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), count.Added, theme.Foreground(chroma.GenericDeleted), count.Removed)
}
