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
	optionRoom := room - 1
	if preview != "" && !wide {
		optionRoom = max(1, (room-2)/2)
	}
	count := min(8, len(options), max(1, optionRoom))
	layout.top = min(question.Selected, min(layout.top, max(0, len(options)-count)))
	if question.Selected >= layout.top+count {
		layout.top = question.Selected - count + 1
	}
	columns := 0
	for _, o := range options[layout.top:min(len(options), layout.top+count)] {
		columns = max(columns, ansi.StringWidth(promptText(o.Label)))
	}
	var optionRows []string
	for i := layout.top; i < min(len(options), layout.top+count); i++ {
		o := options[i]
		label := promptText(o.Label)
		if o.Recommended {
			label = ansi.Truncate(label, max(1, optionWidth-21), "…")
		}
		line := choiceLines(p.Theme, false, i == question.Selected, i+1, label, math.MaxInt)[0]
		if o.Recommended {
			line += " " + accent + "[Recommended]" + reset
		}
		labelWidth := ansi.StringWidth(label)
		if o.Recommended {
			labelWidth += len(" [Recommended]")
		}
		if optionWidth-max(columns, labelWidth)-7 >= 24 {
			line += strings.Repeat(" ", max(0, columns-labelWidth)+2) + dim + promptText(o.Description) + reset
		}
		optionRows = append(optionRows, ansi.Truncate(line, optionWidth, "…"))
	}
	if count < len(options) {
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
			pane = append(pane, dim+ansi.Truncate(heading, previewWidth, "")+reset)
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
