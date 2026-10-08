package diffview

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// liveDiffChanges is the Changes tab: captured changes in capture order, one
// graph lane per caller. Lanes branch from main at the caller's first change.
type liveDiffChanges struct {
	Scope string
	Query string
	Nodes []liveDiffChangeNode
	lanes []string
	Rows  []ChangeRow
	paths []string // File names by view index.
	// workspace displays rename sources.
	workspace string
	Expanded  map[string]bool
	Cursor    int
	Top       int
	// hover is the pointed row plus one; zero points at none.
	Hover int
	// inline names a single-file change's file on the change row, for a
	// navigator too narrow to nest it; otherwise the file is always nested.
	Inline bool
	// target is the change and file that { } last opened.
	Target ChangeTarget
}

type liveDiffChangeNode struct {
	livediff.Origin
	Files   []liveDiffChangeFile
	added   int
	removed int
	unknown bool
}

type liveDiffChangeFile struct {
	File           int
	added, removed int
	unknown        bool
	status         Status
}

type ChangeRow struct {
	Kind byte // 'b' branch, 'c' change, 'f' file
	Node int
	File int // Index into node.files for 'f' rows.
	lane int
	// active counts the lanes that have branched at or before this row.
	active int
}

type ChangeTarget struct{ Change, File string }

// Callers lists the caller filter keys with captures, main first.
func Callers(view *livediff.View) []string {
	var callers []string
	for _, file := range view.Files {
		for _, chunk := range file.Chunks {
			if key := livediff.CallerKey(chunk.Caller); !slices.Contains(callers, key) {
				callers = append(callers, key)
			}
		}
	}
	if i := slices.Index(callers, "/root"); i > 0 {
		callers = slices.Insert(slices.Delete(callers, i, i+1), 0, "/root")
	}
	return callers
}

func (l *liveDiffChanges) Rebuild(view *livediff.View, workspace string) {
	cursor := ChangeTarget{}
	if l.Cursor < len(l.Rows) {
		cursor = l.rowTarget(l.Rows[l.Cursor])
	}
	l.Nodes, l.Rows, l.lanes, l.paths, l.workspace = nil, nil, []string{"/root"}, nil, workspace
	for _, file := range view.Files {
		path := pathdisplay.ForWorkspace(workspace, file.Path)
		if _, name, found := strings.CutLast(path, "/"); found {
			path = name
		}
		l.paths = append(l.paths, path)
	}
	index := make(map[string]int)
	type capture struct {
		chunk livediff.Chunk
		file  int
	}
	var captures []capture
	for i, file := range view.Files {
		for _, chunk := range file.Chunks {
			if view.Shows(chunk) {
				captures = append(captures, capture{chunk, i})
			}
		}
	}
	slices.SortStableFunc(captures, func(a, b capture) int {
		return cmp.Compare(a.chunk.CaptureOrder, b.chunk.CaptureOrder)
	})
	query := strings.ToLower(l.Query)
	for _, capture := range captures {
		chunk := capture.chunk
		if query != "" && !liveDiffChangeMatches(chunk.Origin, query) {
			continue
		}
		id := chunk.Change
		if id == "" {
			id = chunk.Key
		}
		n, found := index[id]
		if !found {
			n = len(l.Nodes)
			index[id] = n
			l.Nodes = append(l.Nodes, liveDiffChangeNode{Origin: chunk.Origin})
			if l.Nodes[n].Change == "" {
				l.Nodes[n].Change = "change"
			}
		}
		node := &l.Nodes[n]
		added, removed := chunk.Review.LineCounts()
		incomplete := chunk.Review.Incomplete != ""
		node.unknown = node.unknown || incomplete
		node.added, node.removed = node.added+added, node.removed+removed
		at := slices.IndexFunc(node.Files, func(file liveDiffChangeFile) bool { return file.File == capture.file })
		if at < 0 {
			at = len(node.Files)
			node.Files = append(node.Files, liveDiffChangeFile{File: capture.file, status: Status{Before: chunk.Review.BeforePath}})
		}
		file := &node.Files[at]
		file.added, file.removed, file.unknown = file.added+added, file.removed+removed, file.unknown || incomplete
		file.status.Add(chunk.Review)
	}
	for n, node := range l.Nodes {
		lane := slices.Index(l.lanes, node.Caller)
		if lane < 0 {
			lane = len(l.lanes)
			l.lanes = append(l.lanes, node.Caller)
			l.Rows = append(l.Rows, ChangeRow{Kind: 'b', Node: n, lane: lane, active: len(l.lanes)})
		}
		l.Rows = append(l.Rows, ChangeRow{Kind: 'c', Node: n, lane: lane, active: len(l.lanes)})
		if len(node.Files) > 1 && l.Expanded[node.Change] || len(node.Files) == 1 && !l.Inline {
			for f := range node.Files {
				l.Rows = append(l.Rows, ChangeRow{Kind: 'f', Node: n, File: f, lane: lane, active: len(l.lanes)})
			}
		}
	}
	l.Cursor = min(l.Cursor, max(0, len(l.Rows)-1))
	l.Top = min(l.Top, max(0, len(l.Rows)-1))
	for i, row := range l.Rows {
		if cursor.Change != "" && l.rowTarget(row) == cursor {
			l.Cursor = i
			break
		}
	}
}

// liveDiffChangeMatches filters by change ID, caller, or source; @name selects a caller.
func liveDiffChangeMatches(origin livediff.Origin, query string) bool {
	caller := "unknown"
	if origin.Caller != "" {
		caller = strings.ToLower(activityui.AgentDisplayName(origin.Caller))
	}
	if name, ok := strings.CutPrefix(query, "@"); ok {
		return strings.Contains(caller, name)
	}
	return strings.Contains(strings.ToLower(origin.Change+" "+origin.Source)+" "+caller, query)
}

func (l *liveDiffChanges) rowTarget(row ChangeRow) ChangeTarget {
	if row.Node >= len(l.Nodes) {
		return ChangeTarget{}
	}
	target := ChangeTarget{Change: l.Nodes[row.Node].Change}
	if row.Kind == 'f' {
		target.File = fmt.Sprint(l.Nodes[row.Node].Files[row.File].File)
	}
	if row.Kind == 'b' {
		target.File = "branch"
	}
	return target
}

func (l *liveDiffChanges) EnsureVisible(rows int) {
	rows = max(1, rows-2)
	l.Top = min(l.Cursor, max(0, min(l.Top, len(l.Rows)-rows)))
	if l.Cursor >= l.Top+rows {
		l.Top = l.Cursor - rows + 1
	}
}

// focusChange moves the cursor to a change's row, keeping its branch row in view.
func (l *liveDiffChanges) FocusChange(change string, rows int) {
	for i, row := range l.Rows {
		if row.Kind == 'c' && l.Nodes[row.Node].Change == change {
			l.Cursor = i
			l.EnsureVisible(rows)
			if i > 0 && l.Rows[i-1].Kind == 'b' && l.Top == i {
				l.Top = i - 1
			}
			return
		}
	}
}

// CallerStyle is how the diff presents a canonical agent path.
func CallerStyle(theme livediff.Theme) func(string) (string, string) {
	return func(caller string) (string, string) {
		if caller == "" || caller == livediff.UnknownCaller {
			return "unknown", livediff.Subtle
		}
		if color := activityui.Color(caller); color != "" {
			return activityui.AgentDisplayName(caller), color
		}
		return activityui.AgentDisplayName(caller), theme.Accent()
	}
}

func (l *liveDiffChanges) Render(focused bool, filtering bool, callerFilter string, width, rows int, theme livediff.Theme) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	style := CallerStyle(theme)
	scope := "@all"
	if callerFilter != "" {
		name, _ := style(callerFilter)
		scope = "@" + name
	}
	heading := fmt.Sprintf(" Changes  %d · %s", len(l.Nodes), livediff.Safe(scope, false))
	if l.Scope != "" {
		heading += " · " + livediff.Safe(l.Scope, false)
	}
	if focused {
		heading = theme.Accent() + "▎" + strings.TrimPrefix(heading, " ") + "\x1b[0m"
	}
	out[0] = ansi.Truncate(heading, max(0, width-1), "…")
	if rows < 2 {
		return out
	}
	filter := " / id, @caller, source"
	if l.Query != "" || filtering {
		filter = " / " + livediff.Safe(l.Query, false)
		if filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate(livediff.Subtle+filter+"\x1b[0m", max(0, width-1), "…")
	// Rows never stay scrolled off the top while space remains below.
	l.Top = min(l.Top, max(0, len(l.Rows)-(rows-2)))
	contentWidth := max(0, width-1)
	// Lanes are two cells wide. Beyond the cap, later callers share the last
	// lane and their rows name the caller instead.
	capped := max(1, min(6, (contentWidth-20)/2))
	laneOf := func(lane int) int { return min(lane, capped-1) }
	laneColor := func(lane int) string {
		_, color := style(l.lanes[lane])
		return color
	}
	for row := 2; row < rows; row++ {
		index := l.Top + row - 2
		if index >= len(l.Rows) {
			if row == 2 && len(l.Rows) == 0 {
				out[row] = " No captured changes"
			}
			break
		}
		entry := l.Rows[index]
		node := l.Nodes[entry.Node]
		active := laneOf(entry.active-1) + 1
		lane := laneOf(entry.lane)
		var graph strings.Builder
		for i := range active {
			glyph, pad := "│", " "
			switch {
			case entry.Kind == 'b' && i == 0:
				glyph, pad = "├", "─"
			case entry.Kind == 'b' && i < lane:
				glyph, pad = "┼", "─"
			case entry.Kind == 'b' && i == lane:
				glyph = "╮"
			case entry.Kind == 'c' && i == lane:
				glyph = "●"
			}
			color := laneColor(min(i, len(l.lanes)-1))
			if entry.Kind == 'b' && i < lane {
				color = laneColor(entry.lane)
			}
			graph.WriteString(color + glyph + pad + "\x1b[0m")
		}
		name, color := style(node.Caller)
		// A change row is label, optional source, then callerTag when its lane is shared.
		label, source, callerTag, stats := "", "", "", ""
		switch entry.Kind {
		case 'b':
			label = color + livediff.Safe(name, false) + "\x1b[0m"
		case 'c':
			label = livediff.Safe(node.Change, false)
			if node.Source != "" {
				source = " " + livediff.Subtle + livediff.Safe(node.Source, false) + livediff.SubtleReset
			}
			if entry.lane != lane {
				callerTag = " " + color + livediff.Safe(name, false) + "\x1b[0m"
			}
			stats = CountStats(livediff.Counts{Added: node.added, Removed: node.removed}, theme)
			if node.unknown {
				stats = CountStats(livediff.Counts{Added: -1, Removed: -1}, theme)
			}
			if len(node.Files) == 1 && l.Inline {
				file := node.Files[0]
				if file.status.Directory {
					stats = ""
				}
				stats = " " + FileLabel(file.status, l.path(file.File), l.workspace, theme) + stats
			} else {
				stats = fmt.Sprintf(" "+livediff.Subtle+"%df"+livediff.SubtleReset, len(node.Files)) + stats
			}
		case 'f':
			file := node.Files[entry.File]
			branch := "├"
			if entry.File == len(node.Files)-1 {
				branch = "└"
			}
			// The tree glyph extends the graph.
			graph.WriteString(livediff.Subtle + branch + livediff.SubtleReset + " ")
			label = FileLabel(file.status, l.path(file.File), l.workspace, theme)
			stats = CountStats(livediff.Counts{Added: file.added, Removed: file.removed}, theme)
			if file.unknown {
				stats = CountStats(livediff.Counts{Added: -1, Removed: -1}, theme)
			}
			if file.status.Directory {
				stats = ""
			}
		}
		prefix := graph.String()
		available := max(0, contentWidth-ansi.StringWidth(prefix)-ansi.StringWidth(stats))
		// The source is optional detail: omit it rather than truncate the row.
		if ansi.StringWidth(label+source+callerTag) > available {
			source = ""
		}
		label += source + callerTag
		body := ansi.Truncate(label, available, "…") + stats
		line := ansi.Truncate(prefix+body, contentWidth, "")
		if focused && index == l.Cursor {
			line = liveDiffSelectRow(line, contentWidth, theme)
		} else if index == l.Hover-1 {
			// An underlined lane would read as a graph edge.
			line = ansi.Truncate(prefix+liveDiffHoverRow(body), contentWidth, "")
		}
		out[row] = line
	}
	return out
}

// path is a file's base name, by view index.
func (l *liveDiffChanges) path(file int) string {
	if file < len(l.paths) {
		return l.paths[file]
	}
	return ""
}
