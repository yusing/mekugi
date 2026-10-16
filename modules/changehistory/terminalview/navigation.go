package terminalview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Navigation is a presentation index. Capture order and evidence stay owned by View.
type Navigation struct {
	presentation.FileTree
	Hidden, Focused, Filtering bool
	Top                        int
	// hover is the pointed entry plus one; zero points at none.
	Hover       int
	Columns     int
	SavedCursor string
	SavedTop    string
	// changes is the Changes tab, shown instead of files when set.
	Changes    Changes
	ChangesTab bool
	// dots marks each file row with its callers, by view index.
	Dots []string
	// caller is the view's caller filter, for the heading.
	Caller string
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

func (n *Navigation) Rebuild(files []presentation.File, workspace string) {
	topID := ""
	if n.Top < len(n.Entries) {
		topID = n.Entries[n.Top].ID
	}
	known := make(map[string]bool, len(n.Entries))
	for _, entry := range n.Entries {
		known[entry.ID] = true
	}
	n.FileTree.Rebuild(files, workspace)
	n.Top = min(n.Top, max(0, len(n.Entries)-1))
	for i, entry := range n.Entries {
		if entry.ID == topID {
			n.Top = i
		}
	}
	// A split compacted chain reveals newly added ancestors above the old top.
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

func (n *Navigation) Render(files []presentation.File, counts []diffmodel.Counts, selected, width, rows int, theme diffrender.Theme) []string {
	out := make([]string, rows)
	if rows == 0 {
		return out
	}
	mode := "tree"
	if n.Flat || n.Query != "" {
		mode = "flat"
	}
	heading := fmt.Sprintf(" Files  %d/%d · %s", len(n.Matches), n.Total, mode)
	if n.Caller != "" {
		name, _ := CallerStyle(theme)(n.Caller)
		heading += " · @" + textsanitize.Safe(name, false)
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
		filter = " / " + textsanitize.Safe(n.Query, false)
		if n.Filtering {
			filter += "▏"
		}
	}
	out[1] = ansi.Truncate(diffrender.Subtle+filter+"\x1b[0m", max(0, width-1), "…")
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
			label = indent + theme.Accent() + arrow + "\x1b[39m " + textsanitize.Safe(entry.Label, false)
			stats = fmt.Sprintf(" "+diffrender.Subtle+"(%d)"+diffrender.SubtleReset, entry.Count)
		} else {
			var regions []diffmodel.ReviewFile
			for _, chunk := range files[entry.File].Chunks {
				regions = append(regions, chunk.Review)
			}
			label = indent + textsanitize.Safe(entry.Label, false)
			if len(regions) > 0 {
				label = indent + diffrender.FileLabel(diffmodel.StatusOf(regions...), entry.Label, n.Workspace, theme)
			}
			if entry.File < len(counts) && !diffmodel.StatusOf(regions...).Directory {
				stats = diffrender.CountStats(counts[entry.File], theme)
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
			out[row] = line + strings.Repeat(" ", max(0, width-2-ansi.StringWidth(line))) + diffrender.Subtle + bar + "\x1b[0m"
		}
	}
	return out
}

// liveDiffSelectRow marks the active row without reserving a marker column.
// Inline style resets restore the fill, and the row fills its available width.
func liveDiffSelectRow(line string, width int, theme diffrender.Theme) string {
	fill := theme.SelectionBackground()
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+fill)
	return fill + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
}

// liveDiffHoverRow underlines the row under the pointer.
func liveDiffHoverRow(line string) string {
	return "\x1b[4m" + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m\x1b[4m") + "\x1b[24m"
}
