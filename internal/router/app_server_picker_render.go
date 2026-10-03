package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Source: codex-rs/tui/src/bottom_pane/{picker_rows,skill_popup,
// skills_toggle_view,list_selection_view}.rs@86be5320b068ef67b56348b02aa8c33706955da6.
// Modal skill workflows replace the composer. Completions keep it visible.
func (u *appServerUI) pickerHeight(width int) int {
	p := &u.picker
	rows := min(8, max(1, len(p.choices)))
	switch p.modal {
	case "menu":
		return 8
	case "manage":
		return len(pickerWrap("Turn skills on or off. Your changes are saved automatically.", max(1, width-4))) + rows + 9
	}
	if p.target.kind == '$' {
		return rows + 3
	}
	return rows + 3 // Files: viewport, overflow spacers, and @! mode hint.
}

func pickerWrap(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func pickerText(text string) string { return strings.ReplaceAll(livediff.Safe(text, false), "\n", " ") }

func (u *appServerUI) pickerSelection() string {
	if u.view != nil && u.view.painter.Colors.HasBackground {
		if u.view.painter.Theme == livediff.LightTheme {
			return "\x1b[1;38;2;0;0;46;48;2;164;205;251m"
		}
		return "\x1b[1;38;2;0;0;46;48;2;99;168;248m"
	}
	return "\x1b[1;7m"
}

func (u *appServerUI) skillsModalFrame(width, height int) []string {
	ph := min(height, u.pickerHeight(width))
	frame := make([]string, height)
	u.mainContentPainted = false
	u.composerRect = terminalRect{}
	if height > ph {
		copy(frame, u.view.render(width, height-ph, time.Now()))
	}
	copy(frame[height-ph:], u.renderPicker(width, ph))
	u.picker.rect = terminalRect{0, height - ph, width, ph}
	return frame
}

func (u *appServerUI) renderPicker(width, height int) []string {
	p := &u.picker
	frame := make([]string, height)
	p.rowStart, p.rowCount = 0, 0
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "")
		}
	}
	bold, dim, reset := "\x1b[1m", activityui.Dim, activityui.Reset
	var tierOverride string
	if p.modal == "settings" && u.settingsChoices == "/tier" {
		tierOverride = effectiveServiceTier(u.model, "", u.serviceTiers)
	}
	if p.modal == "menu" {
		p.top = 0
		if height < 5 {
			labels := []string{"1. List skills", "2. Enable/Disable Skills"}
			put(0, u.pickerSelection()+"› "+labels[p.selected]+reset)
			p.rowStart, p.rowCount, p.top = 0, 1, p.selected
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
			if p.selected == i {
				prefix = "› "
			}
			p.rowStart, p.rowCount = row-i, 2
			text := prefix + labels[i]
			if width-28 >= 24 {
				text += strings.Repeat(" ", 26-ansi.StringWidth(text)) + "  " + descriptions[i]
			}
			if p.selected == i {
				text = u.pickerSelection() + text + reset
			}
			put(row, text)
		}
		put(height-1, "  "+bold+"enter"+reset+dim+" select · "+reset+bold+"esc"+reset+dim+" back"+reset)
		return frame
	}
	first, last := 0, height
	footer := "  " + bold + "enter" + reset + dim + " insert · " + reset + bold + "esc" + reset + dim + " close" + reset
	if p.target.kind == '/' {
		tab := "complete"
		if strings.TrimSpace(u.draft) == "/compact" && p.modal == "" {
			tab = "queue"
		}
		footer = "  " + bold + "↑/↓" + reset + dim + " navigate · " + reset + bold + "enter" + reset + dim + " select · " + reset + bold + "tab" + reset + dim + " " + tab + " · " + reset + bold + "esc" + reset + dim + " close" + reset
	}
	if p.target.kind == '@' {
		footer = "  " + bold + "@!" + reset + dim + " include excluded files" + reset
	}
	if p.modal == "manage" {
		header := append([]string{bold + "Enable/Disable Skills" + reset}, pickerWrap("Turn skills on or off. Your changes are saved automatically.", max(1, width-4))...)
		for i, line := range header {
			put(i+1, "  "+line)
		}
		query := p.query
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
		if p.modal == "settings" {
			title := "Choose " + strings.TrimPrefix(u.settingsChoices, "/")
			if tierOverride != "" {
				title = "Mekugi override: " + tierOverride + " · choose Codex tier"
			}
			put(0, "  "+bold+title+reset)
			footer = "  ↑/↓ navigate · enter apply · esc cancel"
		}
		if p.modal == "copy" {
			put(0, "  "+bold+"Copy to clipboard"+reset)
			footer = "  " + bold + "↑/↓" + reset + dim + " navigate · " + reset + bold + "enter" + reset + dim + " copy · " + reset + bold + "esc" + reset + dim + " close" + reset
		}
		put(height-1, footer)
	}
	if p.modal == "manage" && height < 6 {
		clear(frame)
		first, last = 0, max(1, height-1)
		if height > 1 {
			put(height-1, footer)
		}
	}
	visible := min(8, max(0, last-first))
	if p.modal != "manage" && height < 3 {
		visible = height
		first = 0
	}
	p.top = min(p.selected, min(p.top, max(0, len(p.choices)-visible)))
	if p.selected >= p.top+visible {
		p.top = max(0, p.selected-visible+1)
	}
	p.rowStart, p.rowCount = first, min(visible, max(0, len(p.choices)-p.top))
	if p.top > 0 {
		put(first-1, "↑")
	}
	if p.top+visible < len(p.choices) {
		put(first+visible, "↓")
	}
	if len(p.choices) == 0 {
		message := "no matches"
		if p.loading {
			message = "loading..."
		}
		if p.problem != "" {
			message = p.problem
		}
		put(first, "  "+dim+pickerText(message)+reset)
	} else {
		columns := 0
		for _, choice := range p.choices[p.top:min(len(p.choices), p.top+visible)] {
			label := choice.label()
			if p.modal == "manage" {
				label = "[x] " + label
			}
			if p.target.kind == '$' && p.modal == "" {
				label = pickerTruncateName(label, 28)
			}
			columns = max(columns, ansi.StringWidth(pickerText(label)))
		}
		for row := 0; row < visible && p.top+row < len(p.choices); row++ {
			i := p.top + row
			choice := p.choices[i]
			label := pickerText(choice.label())
			description := pickerText(choice.description)
			if p.modal == "manage" {
				marker := "[ ] "
				if choice.enabled {
					marker = "[x] "
				}
				label = marker + label
			} else if p.target.kind == '$' {
				label = pickerTruncateName(label, 28)
				description = "[Skill] " + description
			} else if p.target.kind == '@' {
				label = pickerFileLabel(choice.path, max(1, width-2))
			}
			prefix := "  "
			if i == p.selected {
				prefix = "› "
			}
			text := prefix + pickerHighlight(label, p.target.query)
			if p.modal == "manage" {
				text = prefix + pickerHighlight(label, p.query)
			}
			if description != "" && width-(columns+4) >= 24 {
				text += strings.Repeat(" ", max(0, columns-ansi.StringWidth(label))+2) + dim + description + reset
			}
			text = ansi.Truncate(text, width, "…")
			if i == p.selected {
				text = u.pickerSelection() + ansi.Strip(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + reset
			}
			put(first+row, text)
		}
		if p.problem != "" {
			put(height-2, "  "+pickerText(p.problem))
		}
	}
	if p.toggleID != "" {
		put(height-2, "  "+dim+"Saving…"+reset)
	}
	if tierOverride != "" && p.problem == "" && !p.loading && height >= 3 {
		put(height-2, "  "+dim+"Choices change Codex's tier only"+reset)
	}
	if p.modal == "manage" {
		// Stock management paints the full panel surface, including its blank rows.
		band := userBand(u.view.painter.Theme)
		for i := 0; i < height-1; i++ {
			frame[i] = band + frame[i] + strings.Repeat(" ", max(0, width-ansi.StringWidth(frame[i]))) + reset
		}
	}
	return frame
}

func pickerTruncateName(text string, n int) string {
	g := uniseg.NewGraphemes(text)
	var out strings.Builder
	for i := 0; g.Next(); i++ {
		if i == n {
			out.WriteString("…")
			break
		}
		out.WriteString(g.Str())
	}
	return out.String()
}

func pickerHighlight(text, query string) string {
	needle := []rune(strings.ToLower(strings.TrimPrefix(query, "!")))
	if len(needle) == 0 {
		return text
	}
	var out strings.Builder
	at := 0
	for _, r := range text {
		if at < len(needle) && strings.EqualFold(string(r), string(needle[at])) {
			fmt.Fprintf(&out, "\x1b[1m%c\x1b[22m", r)
			at++
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func pickerFileLabel(path string, width int) string {
	path = pickerText(path)
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
