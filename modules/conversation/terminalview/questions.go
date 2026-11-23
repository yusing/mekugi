// Source: apps/mekugi/internal/router/app_server_questions.go:619:737@[774b4e24] question dock rendering.
package terminalview

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// QuestionLayout belongs to one answer editor, independent of its host handle.
type QuestionLayout struct {
	Column                 *int
	top, textTop           int
	previewTop, previewMax int
	selected               int
	preview                string
}

func (l *QuestionLayout) focus(q prompt.QuestionView) string {
	preview := ""
	if q.Selected >= 0 && q.Selected < len(q.Choices) {
		preview = q.Choices[q.Selected].Preview
	}
	if l.selected != q.Selected || l.preview != preview {
		l.previewTop, l.previewMax = 0, 0
		l.selected, l.preview = q.Selected, preview
	}
	return preview
}

func optionRowHeight(o prompt.Option, showDesc bool) int {
	if showDesc && promptText(o.Description) != "" {
		return 2
	}
	return 1
}

// optionWindow picks a contiguous slice [start, end) of at most 8 options that
// fits within budget rows while keeping selected visible.
func optionWindow(options []prompt.Option, selected, top, budget int, showDesc bool) (int, int) {
	if len(options) == 0 {
		return 0, 0
	}
	selected = max(0, min(selected, len(options)-1))
	start := max(0, min(top, selected))
	fitEnd := func(from int) int {
		used := 0
		end := from
		for end < len(options) && end-from < 8 {
			h := optionRowHeight(options[end], showDesc)
			if end > from && used+h > budget {
				break
			}
			used += h
			end++
		}
		return end
	}
	end := fitEnd(start)
	for end <= selected && start < selected {
		start++
		end = fitEnd(start)
	}
	// Pull start earlier when scrolled near the bottom and extra rows remain.
	for start > 0 && end-start < 8 {
		prevEnd := fitEnd(start - 1)
		if prevEnd < end || prevEnd <= selected {
			break
		}
		start--
		end = prevEnd
	}
	return start, end
}

// Questions docks the open question as a panel: an accent heading with its
// position, the question, numbered options with a pointer on the selected
// one, and dim keys. Hidden questions collapse to one reminder row.
// caller names the displayed thread's agent; empty means Main.
func Questions(d prompt.QuestionDockView, p *terminalui.Painter, layout *QuestionLayout, caller string, superseding bool, width, height int) []string {
	if d.Count == 0 || height < 1 {
		return nil
	}
	accent, dim, reset := p.Theme.Accent(), terminalui.Dim, terminalui.Reset
	if d.Call == nil {
		questions := fmt.Sprintf("%d question", d.Count)
		if d.Count != 1 {
			questions += "s"
		}
		label := accent + "? " + reset + pathdisplay.AgentDisplayName(cmp.Or(caller, "/root")) + " asks " + questions + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " answer" + reset
		if superseding {
			label = dim + "enter starts a new turn · dismisses " + questions + reset
		}
		padding := min(2, max(0, (width-20)/2))
		return []string{strings.Repeat(" ", padding) + ansi.Truncate(label, width-padding, "…")}
	}
	view := d.Call
	question := view.Questions[d.Index]
	preview := layout.focus(question)
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	state := "open"
	if view.Sync || view.Local {
		state = "waiting"
	}
	position, total := d.Index, len(view.Questions)
	if d.Total > 0 {
		position, total = d.Position, d.Total
	}
	title := fmt.Sprintf("Question %d of %d", position+1, total)
	detail := ""
	if question.Header != question.Text {
		detail = promptText(question.Header)
	}
	rows := []string{terminalui.PanelHeading(accent, title, detail, dim+state+reset, inner)}
	if height >= 10 && (preview == "" || height >= 14) {
		rows = append(rows, "")
	}
	keys := [][2]string{{fmt.Sprintf("1–%d", min(9, len(question.Choices)+1)), "choose"}, {"type", "answer"}}
	if view.Local {
		keys = [][2]string{{"type", "password"}, {"enter", "submit"}}
	}
	arrows := "question"
	if question.Note || question.Selected == len(question.Choices) {
		arrows = "move"
	}
	if question.Selected < len(question.Choices) {
		keys = append(keys, [2]string{"tab", "note"})
	}
	if total > 1 {
		keys = append(keys, [2]string{"enter", "next"}, [2]string{"←/→", arrows})
	}
	keys = append(keys, [2]string{"ctrl+]", "skip"}, [2]string{"esc", "hide"})
	if preview != "" {
		keys = append(keys, [2]string{"alt+shift+↑/↓", "preview"})
	}
	hints := func(pairs [][2]string) []string { return panelKeys(pairs, inner) }
	foot := hints(keys)
	if len(foot) > max(2, height/3) {
		compact := [][2]string{{"↑↓", "pick"}}
		if total > 1 {
			compact = append(compact, [2]string{"↵", "next"}, [2]string{"←→", arrows})
		}
		compact = append(compact, [2]string{"^]", "skip"}, [2]string{"esc", "hide"})
		if question.Selected < len(question.Choices) {
			compact = append(compact, [2]string{"tab", "note"})
		}
		if preview != "" {
			compact = append(compact, [2]string{"alt+shift+↑/↓", "preview"})
		}
		foot = hints(compact)
	}
	if d.Confirm {
		hint := " · enter submit · esc back"
		if preview != "" {
			hint += " · alt+shift+↑/↓ preview"
		}
		foot = promptWrap(accent+fmt.Sprintf("Submit with %d unanswered?", d.Gaps)+reset+dim+hint+reset, inner)
	}
	text := promptWrap(textsanitize.Safe(question.Text, false), max(1, inner-2))
	for i, line := range text {
		text[i] = "  " + line
	}
	textRoom := max(1, height-len(rows)-len(foot)-5)
	if preview != "" {
		textRoom = max(1, textRoom-4)
	}
	layout.textTop = min(layout.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[layout.textTop:min(len(text), layout.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, "  "+dim+ansi.Truncate("pgup/pgdn question text", inner-2, "")+reset)
	}
	if height >= 10 && (preview == "" || height >= 14) {
		rows = append(rows, "")
	}
	options := append(slices.Clone(question.Choices), prompt.Option{Label: "Other", Description: "Type your answer"})
	optionWidth := inner
	wide := preview != "" && width >= 80
	if wide {
		optionWidth = (inner - 3) / 2
	}
	room := max(1, height-len(rows)-len(foot)-1)
	if preview != "" {
		room = max(1, height-len(rows)-len(foot))
	}
	optionRoom := max(1, room-1)
	if preview != "" && !wide {
		optionRoom = max(1, (room-2)/2)
	}
	allRows := 0
	for _, o := range options {
		allRows += optionRowHeight(o, true)
	}
	var start, end int
	showDesc := true
	if len(options) <= 8 && allRows <= room && (preview == "" || wide || allRows <= optionRoom) {
		start, end = 0, len(options)
		layout.top = 0
	} else {
		budget := optionRoom
		if preview != "" && !wide {
			budget = max(1, optionRoom-1)
		}
		if budget < 2 {
			showDesc = false
		}
		start, end = optionWindow(options, question.Selected, layout.top, budget, showDesc)
		layout.top = start
	}
	var optionRows []string
	for i := start; i < end; i++ {
		o := options[i]
		label := promptText(o.Label)
		prefixLen := 2 + len(fmt.Sprintf("%d. ", i+1))
		if o.Recommended {
			label = ansi.Truncate(label, max(1, optionWidth-prefixLen-len(" [Recommended]")), "…")
		}
		line := choiceLines(p.Theme, false, i == question.Selected, i+1, label, math.MaxInt)[0]
		if o.Recommended {
			line += " " + accent + "[Recommended]" + reset
		}
		optionRows = append(optionRows, ansi.Truncate(line, optionWidth, "…"))
		if desc := promptText(o.Description); showDesc && desc != "" && optionWidth > prefixLen {
			indent := strings.Repeat(" ", prefixLen)
			optionRows = append(optionRows, indent+dim+ansi.Truncate(desc, optionWidth-prefixLen, "…")+reset)
		}
	}
	if end-start < len(options) {
		optionRows = append(optionRows, "  "+dim+ansi.Truncate(fmt.Sprintf("option %d/%d", question.Selected+1, len(options)), optionWidth-2, "")+reset)
	}
	if preview == "" {
		rows = append(rows, optionRows...)
	} else {
		if !wide {
			optionRows = optionRows[:min(len(optionRows), max(0, room-1))]
		}
		previewWidth, previewRoom := inner, max(1, room-len(optionRows))
		if wide {
			previewWidth, previewRoom = inner-optionWidth-3, room
		}
		lines := promptWrap(textsanitize.Safe(preview, false), previewWidth)
		visible := max(1, previewRoom-1)
		layout.previewMax = max(0, len(lines)-visible)
		layout.previewTop = min(layout.previewTop, layout.previewMax)
		heading := fmt.Sprintf("Preview %d–%d/%d", layout.previewTop+1, min(len(lines), layout.previewTop+visible), len(lines))
		var pane []string
		if previewRoom > 1 {
			if wide {
				pane = append(pane, dim+ansi.Truncate(heading, previewWidth, "")+reset)
			} else {
				rule := "── " + heading + " "
				if fill := previewWidth - ansi.StringWidth(rule); fill > 0 {
					rule += strings.Repeat("─", fill)
				}
				pane = append(pane, dim+ansi.Truncate(rule, previewWidth, "")+reset)
			}
		}
		pane = append(pane, lines[layout.previewTop:min(len(lines), layout.previewTop+visible)]...)
		if wide {
			for i := 0; i < max(len(optionRows), len(pane)); i++ {
				left, right := "", ""
				if i < len(optionRows) {
					left = optionRows[i]
				}
				if i < len(pane) {
					right = pane[i]
				}
				rows = append(rows, left+strings.Repeat(" ", max(0, optionWidth-ansi.StringWidth(left)))+dim+" │ "+reset+right)
			}
		} else {
			rows = append(rows, optionRows...)
			rows = append(rows, pane...)
		}
	}
	if len(rows)+len(foot) < height {
		rows = append(rows, "")
	}
	rows = append(rows, foot...)
	for i := range rows {
		rows[i] = strings.Repeat(" ", padding) + rows[i]
	}
	return rows[:min(len(rows), height)]
}
