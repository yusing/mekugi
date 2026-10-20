package terminalsession

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"strings"
	"time"
)

func resumePickerAge(now time.Time, updated int64) string {
	if updated == 0 {
		return "-"
	}
	d := now.Sub(time.Unix(updated, 0))
	if d < time.Second {
		return "now"
	}
	return activityui.FormatDuration(d) + " ago"
}

// The picker replaces Main, as the stock resume screen replaces the chat.
// Column widths come from the visible rows only.
// Source: apps/mekugi/internal/router/app_server_resume_picker.go:302:462@630ce384.
func (p *ResumePicker) Render(painter *terminalui.Painter, width, height int, selection string) []string {
	view := p.body
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "…")
		}
	}
	bold, dim, reset := "\x1b[1m", terminalui.Dim, terminalui.Reset
	accent := "\x1b[38;2;104;174;245m"
	if painter.Theme == diffrender.LightTheme {
		accent = "\x1b[38;2;25;94;164m"
	}
	option := func(label string, on bool) string {
		if on {
			return bold + accent + label + reset
		}
		return dim + label + reset
	}
	put(0, "  "+bold+"Resume a previous session"+reset)
	filter := "  " + dim + "Filter:" + reset + " "
	if view.Workspace != "" {
		filter += option("Cwd", !view.All) + " " + option("All", view.All)
		if !view.All {
			filter += dim + " · " + reset + panelText(view.Workspace)
		}
	} else {
		filter += option("All", true)
	}
	put(1, filter)
	search := "  " + dim + "Type to search" + reset
	if view.Query != "" {
		search = "  Search: " + panelText(view.Query)
	}
	if view.Problem != "" {
		search = "  \x1b[38;2;239;117;117m" + panelText(view.Problem) + reset
	}
	put(2, search)
	footer := "  " + bold + "enter" + reset + dim + " resume · " + reset + bold + "esc" + reset + dim
	switch {
	case view.Query != "":
		footer += " clear search"
	case view.Startup:
		footer += " start new"
	default:
		footer += " close"
	}
	if view.Workspace != "" {
		footer += " · " + reset + bold + "tab" + reset + dim + " cwd/all"
	}
	footer += " · " + reset + bold + "ctrl+c" + reset + dim
	if view.Startup {
		footer += " quit"
	} else {
		footer += " close"
	}
	footer += " · " + reset + bold + "↑/↓" + reset + dim + " browse" + reset
	put(height-1, footer)

	rows := view.Rows
	first, last := 5, height-2 // Header row at 4; the row before the footer marks more.
	if height < 8 {
		first, last = min(3, height-1), height-1
	}
	p.Viewport = max(1, last-first)
	p.Selected = max(0, min(p.Selected, len(rows)-1))
	p.Top = max(0, min(p.Top, p.Selected, len(rows)-p.Viewport))
	if p.Selected >= p.Top+p.Viewport {
		p.Top = p.Selected - p.Viewport + 1
	}
	p.RowStart = first
	if len(rows) == 0 {
		message := "No sessions yet"
		switch {
		case view.Loading && view.Query != "":
			message = "Searching…"
		case view.Loading:
			message = "Loading sessions…"
		case view.Query != "" && view.HasMore:
			message = fmt.Sprintf("Search scanned first %d sessions; more may exist", view.Scanned)
		case view.Query != "":
			message = "No results for your search"
		}
		put(first, "  "+dim+message+reset)
		return frame
	}
	visible := rows[p.Top:min(len(rows), p.Top+p.Viewport)]
	ageWidth, branchWidth, cwdWidth := len("Updated"), len("Branch"), len("Directory")
	for _, row := range visible {
		ageWidth = max(ageWidth, ansi.StringWidth(resumePickerAge(view.LoadedAt, row.Updated)))
		branchWidth = max(branchWidth, ansi.StringWidth(panelText(row.Branch)))
		cwdWidth = max(cwdWidth, ansi.StringWidth(panelText(pathdisplay.ForWorkspace(view.Workspace, row.Workspace))))
	}
	branchWidth, cwdWidth = min(branchWidth, 24), min(cwdWidth, max(12, width/3))
	showBranch := width >= 60
	showCwd := view.All && width >= 90
	pad := func(text string, n int) string {
		text = ansi.Truncate(text, n, "…")
		return text + strings.Repeat(" ", max(0, n-ansi.StringWidth(text)))
	}
	columns := func(age, branch, cwd, title string) string {
		text := pad(age, ageWidth) + "  "
		if showBranch {
			text += pad(branch, branchWidth) + "  "
		}
		if showCwd {
			text += pad(cwd, cwdWidth) + "  "
		}
		return text + title
	}
	if first > 3 {
		header := columns("Updated", "Branch", "Directory", "Conversation")
		if p.Top > 0 {
			header += "  ↑ more"
		}
		put(first-1, "  "+dim+header+reset)
	}
	for i, row := range visible {
		index := p.Top + i
		title := panelText(row.Title)
		if title == "" {
			title = "(no message yet)"
		}
		if row.ID == p.main {
			title += dim + " · current" + reset
		}
		prefix := "  "
		if index == p.Selected {
			prefix = "› "
		}
		text := prefix + columns(resumePickerAge(view.LoadedAt, row.Updated), panelText(row.Branch), panelText(pathdisplay.ForWorkspace(view.Workspace, row.Workspace)), title)
		text = ansi.Truncate(text, width, "…")
		if index == p.Selected {
			text = selection + ansi.Strip(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + reset
		}
		put(first+i, text)
	}
	switch {
	case last == height-1: // The footer keeps its row in a short pane.
	case view.Loading:
		put(last, "  "+dim+"↓ loading more"+reset)
	case p.Top+p.Viewport < len(rows) || view.HasMore && view.Query == "":
		put(last, "  "+dim+"↓ more"+reset)
	}
	return frame
}
