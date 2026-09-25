package router

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// liveDiffChanges is the Changes tab: unreviewed changes in capture order, one
// graph lane per caller. Lanes branch from main at the caller's first change.
type liveDiffChanges struct {
	query string
	nodes []liveDiffChangeNode
	lanes []string
	rows  []liveDiffChangeRow
	paths []string // File names by view index.
	// workspace displays rename sources.
	workspace string
	expanded  map[string]bool
	cursor    int
	top       int
	// target is the change and file that { } last opened.
	target liveDiffChangeTarget
}

type liveDiffChangeNode struct {
	livediff.Origin
	files   []liveDiffChangeFile
	state   byte // 'a' applied, 'o' observed, 'x' incomplete
	added   int
	removed int
	unknown bool
}

type liveDiffChangeFile struct {
	file           int
	added, removed int
	unknown        bool
	status         liveDiffStatus
}

type liveDiffChangeRow struct {
	kind byte // 'b' branch, 'c' change, 'f' file
	node int
	file int // Index into node.files for 'f' rows.
	lane int
	// active counts the lanes that have branched at or before this row.
	active int
}

type liveDiffChangeTarget struct{ change, file string }

// liveDiffCallers lists the caller filter keys with unreviewed captures, main first.
func liveDiffCallers(view *liveDiffView) []string {
	var callers []string
	for _, file := range view.Files {
		for _, chunk := range file.Chunks {
			if key := livediff.CallerKey(chunk.Caller); !view.Reviewed[chunk.Key] && !slices.Contains(callers, key) {
				callers = append(callers, key)
			}
		}
	}
	if i := slices.Index(callers, "/root"); i > 0 {
		callers = slices.Insert(slices.Delete(callers, i, i+1), 0, "/root")
	}
	return callers
}

func (l *liveDiffChanges) rebuild(view *liveDiffView, workspace string) {
	cursor := liveDiffChangeTarget{}
	if l.cursor < len(l.rows) {
		cursor = l.rowTarget(l.rows[l.cursor])
	}
	l.nodes, l.rows, l.lanes, l.paths, l.workspace = nil, nil, []string{"/root"}, nil, workspace
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
			if !view.Reviewed[chunk.Key] && view.Shows(chunk) {
				captures = append(captures, capture{chunk, i})
			}
		}
	}
	slices.SortStableFunc(captures, func(a, b capture) int {
		return cmp.Compare(a.chunk.CaptureOrder, b.chunk.CaptureOrder)
	})
	query := strings.ToLower(l.query)
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
			n = len(l.nodes)
			index[id] = n
			l.nodes = append(l.nodes, liveDiffChangeNode{Origin: chunk.Origin, state: 'a'})
			if l.nodes[n].Change == "" {
				l.nodes[n].Change = "change"
			}
		}
		node := &l.nodes[n]
		added, removed := chunk.Review.LineCounts()
		incomplete := chunk.Review.Incomplete != ""
		switch {
		case incomplete:
			node.state = 'x'
		case !chunk.Applied && node.state == 'a':
			node.state = 'o'
		}
		node.unknown = node.unknown || incomplete
		node.added, node.removed = node.added+added, node.removed+removed
		at := slices.IndexFunc(node.files, func(file liveDiffChangeFile) bool { return file.file == capture.file })
		if at < 0 {
			at = len(node.files)
			node.files = append(node.files, liveDiffChangeFile{file: capture.file, status: liveDiffStatus{before: chunk.Review.BeforePath}})
		}
		file := &node.files[at]
		file.added, file.removed, file.unknown = file.added+added, file.removed+removed, file.unknown || incomplete
		file.status.add(chunk.Review)
	}
	for n, node := range l.nodes {
		lane := slices.Index(l.lanes, node.Caller)
		if lane < 0 {
			lane = len(l.lanes)
			l.lanes = append(l.lanes, node.Caller)
			l.rows = append(l.rows, liveDiffChangeRow{kind: 'b', node: n, lane: lane, active: len(l.lanes)})
		}
		l.rows = append(l.rows, liveDiffChangeRow{kind: 'c', node: n, lane: lane, active: len(l.lanes)})
		if l.expanded[node.Change] {
			for f := range node.files {
				l.rows = append(l.rows, liveDiffChangeRow{kind: 'f', node: n, file: f, lane: lane, active: len(l.lanes)})
			}
		}
	}
	l.cursor = min(l.cursor, max(0, len(l.rows)-1))
	l.top = min(l.top, max(0, len(l.rows)-1))
	for i, row := range l.rows {
		if cursor.change != "" && l.rowTarget(row) == cursor {
			l.cursor = i
			break
		}
	}
}

// liveDiffChangeMatches filters by change ID, caller, or source; @name selects a caller.
func liveDiffChangeMatches(origin livediff.Origin, query string) bool {
	caller := "unknown"
	if origin.Caller != "" {
		caller = strings.ToLower(agentDisplayName(origin.Caller))
	}
	if name, ok := strings.CutPrefix(query, "@"); ok {
		return strings.Contains(caller, name)
	}
	return strings.Contains(strings.ToLower(origin.Change+" "+origin.Source)+" "+caller, query)
}

func (l *liveDiffChanges) rowTarget(row liveDiffChangeRow) liveDiffChangeTarget {
	if row.node >= len(l.nodes) {
		return liveDiffChangeTarget{}
	}
	target := liveDiffChangeTarget{change: l.nodes[row.node].Change}
	if row.kind == 'f' {
		target.file = fmt.Sprint(l.nodes[row.node].files[row.file].file)
	}
	if row.kind == 'b' {
		target.file = "branch"
	}
	return target
}

func (l *liveDiffChanges) ensureVisible(rows int) {
	rows = max(1, rows-2)
	l.top = min(l.cursor, max(0, min(l.top, len(l.rows)-rows)))
	if l.cursor >= l.top+rows {
		l.top = l.cursor - rows + 1
	}
}

// focusChange moves the cursor to a change's row, keeping its branch row in view.
func (l *liveDiffChanges) focusChange(change string, rows int) {
	for i, row := range l.rows {
		if row.kind == 'c' && l.nodes[row.node].Change == change {
			l.cursor = i
			l.ensureVisible(rows)
			if i > 0 && l.rows[i-1].kind == 'b' && l.top == i {
				l.top = i - 1
			}
			return
		}
	}
}

// liveDiffCallerStyle is how the diff presents a canonical agent path.
func liveDiffCallerStyle(theme livediff.Theme) func(string) (string, string) {
	return func(caller string) (string, string) {
		if caller == "" || caller == livediff.UnknownCaller {
			return "unknown", "\x1b[2m"
		}
		if color := liveAgentColor(caller); color != "" {
			return agentDisplayName(caller), color
		}
		return agentDisplayName(caller), theme.Accent()
	}
}

func (l *liveDiffChanges) render(focused bool, filtering bool, callerFilter string, width, rows int, theme livediff.Theme) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	style := liveDiffCallerStyle(theme)
	scope := "@all"
	if callerFilter != "" {
		name, _ := style(callerFilter)
		scope = "@" + name
	}
	heading := fmt.Sprintf(" Changes  %d · %s", len(l.nodes), livediff.Safe(scope, false))
	if focused {
		heading = theme.Accent() + "▎" + strings.TrimPrefix(heading, " ") + "\x1b[0m"
	}
	out[0] = ansi.Truncate(heading, max(0, width-1), "…")
	if rows < 2 {
		return out
	}
	filter := " / id, @caller, source"
	if l.query != "" || filtering {
		filter = " / " + livediff.Safe(l.query, false)
		if filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate("\x1b[2m"+filter+"\x1b[0m", max(0, width-1), "…")
	// Rows never stay scrolled off the top while space remains below.
	l.top = min(l.top, max(0, len(l.rows)-(rows-2)))
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
		index := l.top + row - 2
		if index >= len(l.rows) {
			if row == 2 && len(l.rows) == 0 {
				out[row] = " No unreviewed changes"
			}
			break
		}
		entry := l.rows[index]
		node := l.nodes[entry.node]
		active := laneOf(entry.active-1) + 1
		lane := laneOf(entry.lane)
		var graph strings.Builder
		for i := range active {
			glyph, pad := "│", " "
			switch {
			case entry.kind == 'b' && i == 0:
				glyph, pad = "├", "─"
			case entry.kind == 'b' && i < lane:
				glyph, pad = "┼", "─"
			case entry.kind == 'b' && i == lane:
				glyph = "╮"
			case entry.kind == 'c' && i == lane:
				glyph = "●"
			}
			color := laneColor(min(i, len(l.lanes)-1))
			if entry.kind == 'b' && i < lane {
				color = laneColor(entry.lane)
			}
			graph.WriteString(color + glyph + pad + "\x1b[0m")
		}
		marker := " "
		if focused && index == l.cursor {
			marker = theme.Accent() + ">" + "\x1b[0m"
		}
		name, color := style(node.Caller)
		// A change row is label, optional source, then callerTag when its lane is shared.
		label, source, callerTag, stats := "", "", "", ""
		switch entry.kind {
		case 'b':
			label = color + livediff.Safe(name, false) + "\x1b[0m"
		case 'c':
			label = livediff.Safe(node.Change, false)
			if node.Source != "" {
				source = " \x1b[2m" + livediff.Safe(node.Source, false) + "\x1b[22m"
			}
			if entry.lane != lane {
				callerTag = " " + color + livediff.Safe(name, false) + "\x1b[0m"
			}
			stats = liveDiffCountStats(livediff.Counts{Added: node.added, Removed: node.removed}, theme)
			if node.unknown {
				stats = liveDiffCountStats(livediff.Counts{Added: -1, Removed: -1}, theme)
			}
			stats = fmt.Sprintf(" \x1b[2m%df\x1b[22m", len(node.files)) + stats + " " + liveDiffChangeGlyph(node.state)
		case 'f':
			file := node.files[entry.file]
			branch := "├"
			if entry.file == len(node.files)-1 {
				branch = "└"
			}
			label = "\x1b[2m" + branch + "\x1b[22m " + liveDiffFileLabel(file.status, l.path(file.file), l.workspace, theme)
			stats = liveDiffCountStats(livediff.Counts{Added: file.added, Removed: file.removed}, theme)
			if file.unknown {
				stats = liveDiffCountStats(livediff.Counts{Added: -1, Removed: -1}, theme)
			}
		}
		prefix := marker + graph.String()
		available := max(0, contentWidth-ansi.StringWidth(prefix)-ansi.StringWidth(stats))
		// The source is optional detail: omit it rather than truncate the row.
		if ansi.StringWidth(label+source+callerTag) > available {
			source = ""
		}
		label += source + callerTag
		out[row] = ansi.Truncate(prefix+ansi.Truncate(label, available, "…")+stats, contentWidth, "")
	}
	return out
}

func liveDiffChangeGlyph(state byte) string {
	switch state {
	case 'a':
		return liveActivityGreen + "✓" + liveActivityReset
	case 'x':
		return "\x1b[31m✗\x1b[39m"
	}
	return "\x1b[2m◌\x1b[22m"
}

// path is a file's base name, by view index.
func (l *liveDiffChanges) path(file int) string {
	if file < len(l.paths) {
		return l.paths[file]
	}
	return ""
}
