package terminalview

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Changes is the Changes tab: captured changes in capture order, one
// graph lane per caller. Lanes branch from main at the caller's first change.
type Changes struct {
	presentation.Changes
	Top, Hover, Cursor int
	Rows               []ChangeRow
	lanes              []string
	// Inline is the narrow-pane layout, with one-file changes on the change row.
	Inline bool
}

type ChangeRow struct {
	Kind byte // 'b' branch, 'c' change, 'f' file
	Node int
	File int // Index into node.files for 'f' rows.
	lane int
	// Active counts the Lanes that have branched at or before this row.
	active int
}

func (l *Changes) Rebuild(view *presentation.View, workspace string) {
	cursor := presentation.ChangeTarget{}
	if l.Cursor < len(l.Rows) {
		cursor = l.rowTarget(l.Rows[l.Cursor])
	}
	l.Changes.Rebuild(view, workspace, pathdisplay.AgentDisplayName)
	l.Rows, l.lanes = nil, []string{"/root"}
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
	for i, row := range l.Rows {
		if cursor.Change != "" && l.rowTarget(row) == cursor {
			l.Cursor = i
			break
		}
	}
	l.Top = min(l.Top, max(0, len(l.Rows)-1))
}

func (l *Changes) rowTarget(row ChangeRow) presentation.ChangeTarget {
	if row.Node >= len(l.Nodes) {
		return presentation.ChangeTarget{}
	}
	target := presentation.ChangeTarget{Change: l.Nodes[row.Node].Change}
	if row.Kind == 'f' {
		target.File = fmt.Sprint(l.Nodes[row.Node].Files[row.File].File)
	}
	if row.Kind == 'b' {
		target.File = "branch"
	}
	return target
}
func (l *Changes) EnsureVisible(rows int) {
	rows = max(1, rows-2)
	l.Top = min(l.Cursor, max(0, min(l.Top, len(l.Rows)-rows)))
	if l.Cursor >= l.Top+rows {
		l.Top = l.Cursor - rows + 1
	}
}

// FocusChange moves the cursor to a change's row, keeping its branch row in view.
func (l *Changes) FocusChange(change string, rows int) {
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
func CallerStyle(theme diffrender.Theme) func(string) (string, string) {
	return func(caller string) (string, string) {
		if caller == "" || caller == presentation.UnknownCaller {
			return "unknown", diffrender.Subtle
		}
		if color := terminalui.AgentColor(caller); color != "" {
			return pathdisplay.AgentDisplayName(caller), color
		}
		return pathdisplay.AgentDisplayName(caller), theme.Accent()
	}
}

func (l *Changes) Render(focused bool, filtering bool, callerFilter string, width, rows int, theme diffrender.Theme) []string {
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
	heading := fmt.Sprintf(" Changes  %d · %s", len(l.Nodes), textsanitize.Safe(scope, false))
	if focused {
		heading = theme.Accent() + "▎" + strings.TrimPrefix(heading, " ") + "\x1b[0m"
	}
	out[0] = ansi.Truncate(heading, max(0, width-1), "…")
	if rows < 2 {
		return out
	}
	filter := " / id, @caller, source"
	if l.Query != "" || filtering {
		filter = " / " + textsanitize.Safe(l.Query, false)
		if filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate(diffrender.Subtle+filter+"\x1b[0m", max(0, width-1), "…")
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
			label = color + textsanitize.Safe(name, false) + "\x1b[0m"
		case 'c':
			label = textsanitize.Safe(node.Change, false)
			if node.Source != "" {
				source = " " + diffrender.Subtle + textsanitize.Safe(node.Source, false) + diffrender.SubtleReset
			}
			if entry.lane != lane {
				callerTag = " " + color + textsanitize.Safe(name, false) + "\x1b[0m"
			}
			stats = diffrender.CountStats(diffmodel.Counts{Added: node.Added, Removed: node.Removed}, theme)
			if node.Unknown {
				stats = diffrender.CountStats(diffmodel.Counts{Added: -1, Removed: -1}, theme)
			}
			if len(node.Files) == 1 && l.Inline {
				file := node.Files[0]
				if file.Status.Directory {
					stats = ""
				}
				stats = " " + diffrender.FileLabel(file.Status, l.path(file.File), l.Workspace, theme) + stats
			} else {
				stats = fmt.Sprintf(" "+diffrender.Subtle+"%df"+diffrender.SubtleReset, len(node.Files)) + stats
			}
		case 'f':
			file := node.Files[entry.File]
			branch := "├"
			if entry.File == len(node.Files)-1 {
				branch = "└"
			}
			// The tree glyph extends the graph.
			graph.WriteString(diffrender.Subtle + branch + diffrender.SubtleReset + " ")
			label = diffrender.FileLabel(file.Status, l.path(file.File), l.Workspace, theme)
			stats = diffrender.CountStats(diffmodel.Counts{Added: file.Added, Removed: file.Removed}, theme)
			if file.Unknown {
				stats = diffrender.CountStats(diffmodel.Counts{Added: -1, Removed: -1}, theme)
			}
			if file.Status.Directory {
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
func (l *Changes) path(file int) string {
	if file < len(l.Paths) {
		return l.Paths[file]
	}
	return ""
}
