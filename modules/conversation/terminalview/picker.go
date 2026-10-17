// Source: apps/mekugi/internal/router/app_server_picker_render.go:69:314@[0a356401] composer completion and skill-management painting.
package terminalview

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

type PickerOptions struct {
	Selected                                 int
	SkillsMenu, Manage, Saving, QueueCompact bool
	Selection, Band                          string
}

func PickerHeight(choices int, options PickerOptions, width int) int {
	if options.SkillsMenu {
		return 8
	}
	rows := min(8, max(1, choices))
	if options.Manage {
		return len(promptWrap("Turn skills on or off. Your changes are saved automatically.", max(1, width-4))) + rows + 9
	}
	return rows + 3
}

func Picker(p completion.View, layout *terminalui.ChoiceListLayout, options PickerOptions, width, height int) []string {
	frame := make([]string, height)
	layout.RowStart, layout.RowCount = 0, 0
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	bold, dim, reset := "\x1b[1m", terminalui.Dim, terminalui.Reset
	if options.SkillsMenu {
		layout.Top = 0
		if height < 5 {
			labels := []string{"1. List skills", "2. Enable/Disable Skills"}
			put(0, options.Selection+"› "+labels[options.Selected]+reset)
			layout.RowStart, layout.RowCount, layout.Top = 0, 1, options.Selected
			if height > 1 {
				put(height-1, "  enter select · esc back")
			}
			return frame
		}
		// The stock action menu is a compact numbered picker, not a skill list.
		put(0, "  "+bold+"Skills"+reset)
		put(1, "  "+dim+"Choose an action"+reset)
		labels := []string{"1. List skills", "2. Enable/Disable Skills"}
		descriptions := []string{"Tip: press $ to open this list directly", "Enable or disable skills"}
		for i := range labels {
			row := 4 + i
			if height < 8 {
				row = max(1, height-4) + i
			}
			prefix := "  "
			if options.Selected == i {
				prefix = "› "
			}
			layout.RowStart, layout.RowCount = row-i, 2
			text := prefix + labels[i]
			if width-28 >= 24 {
				text += strings.Repeat(" ", 26-ansi.StringWidth(text)) + "  " + descriptions[i]
			}
			if options.Selected == i {
				text = options.Selection + text + reset
			}
			put(row, text)
		}
		put(height-1, "  "+bold+"enter"+reset+dim+" select · "+reset+bold+"esc"+reset+dim+" back"+reset)
		return frame
	}
	first, last := 0, height
	footer := "  " + bold + "enter" + reset + dim + " insert · " + reset + bold + "esc" + reset + dim + " close" + reset
	if p.Target.Kind == '/' {
		tab := "complete"
		if options.QueueCompact && !options.Manage && !options.SkillsMenu {
			tab = "queue"
		}
		footer = "  " + bold + "↑/↓" + reset + dim + " navigate · " + reset + bold + "enter" + reset + dim + " select · " + reset + bold + "tab" + reset + dim + " " + tab + " · " + reset + bold + "esc" + reset + dim + " close" + reset
	}
	if p.Target.Kind == '@' {
		footer = "  " + bold + "@!" + reset + dim + " include excluded files" + reset
	}
	if options.Manage {
		header := append([]string{bold + "Enable/Disable Skills" + reset}, promptWrap("Turn skills on or off. Your changes are saved automatically.", max(1, width-4))...)
		for i, line := range header {
			put(i+1, "  "+line)
		}
		query := p.Query
		if query == "" {
			query = "Type to search skills"
		}
		first = len(header) + 4
		if height < first+3 {
			first = max(1, height-3)
		}
		put(first-2, "  "+dim+query+reset)
		last = height - 3
		footer = "  " + bold + "space/enter" + reset + dim + " toggle · " + reset + bold + "esc" + reset + dim + " close" + reset
	} else {
		if height >= 3 {
			first = 1
			last = height - 2
		}
	}
	if height > 1 {

		put(height-1, footer)
	}
	if options.Manage && height < 6 {
		clear(frame)
		first, last = 0, max(1, height-1)
		if height > 1 {
			put(height-1, footer)
		}
	}
	visible := min(8, max(0, last-first))
	if !options.Manage && height < 3 {
		visible = height
		first = 0
	}
	rows := make([]terminalui.ChoiceRow, len(p.Choices))
	for i, choice := range p.Choices {
		label, description := promptText(choice.Label()), promptText(choice.Description)
		if options.Manage {
			marker := "[ ] "
			if choice.Enabled {
				marker = "[x] "
			}
			label = marker + label
		} else if p.Target.Kind == '$' {
			label = terminalui.TruncateGraphemes(label, 28)
			description = "[Skill] " + description
		} else if p.Target.Kind == '@' {
			label = fileLabel(choice.Path, max(1, width-2))
		}
		rows[i] = terminalui.ChoiceRow{Text: label, Description: description}
	}
	query := p.Target.Query
	if options.Manage {
		query = p.Query
	}
	layout.Draw(frame, width, first, visible, terminalui.ChoiceListView{Rows: rows, Selected: options.Selected, Query: query, Selection: options.Selection, Problem: p.Problem, Loading: p.Loading})
	if options.Saving {
		put(height-2, "  "+dim+"Saving…"+reset)
	}
	if options.Manage {
		// Stock management paints the full panel surface, including its blank rows.
		band := options.Band
		for i := 0; i < height-1; i++ {
			frame[i] = band + frame[i] + strings.Repeat(" ", max(0, width-ansi.StringWidth(frame[i]))) + reset
		}
	}
	return frame
}

func fileLabel(path string, width int) string {
	path = promptText(path)
	if ansi.StringWidth(path) <= width {
		return path
	}
	name := filepath.Base(path)
	if ansi.StringWidth(name)+2 <= width {
		return ansi.Truncate(filepath.Dir(path), width-ansi.StringWidth(name)-1, "…") + string(filepath.Separator) + name
	}
	left := max(0, (width-1)/2)
	right := max(0, width-1-left)
	return ansi.Cut(name, 0, left) + "…" + ansi.Cut(name, max(0, ansi.StringWidth(name)-right), ansi.StringWidth(name))
}
