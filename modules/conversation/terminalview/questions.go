// Source: apps/mekugi/internal/router/app_server_questions.go:619:737@[774b4e24] question dock rendering.
package terminalview

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// QuestionLayout belongs to one answer editor, independent of its host handle.
type QuestionLayout struct {
	Column       *int
	top, textTop int
}

// Questions docks the open question as a panel: an accent heading with its
// position, the question, numbered options with a pointer on the selected
// one, and dim keys. Hidden questions collapse to one reminder row.
func Questions(d prompt.QuestionDockView, p *terminalui.Painter, layout *QuestionLayout, superseding bool, width, height int) []string {
	if d.Count == 0 || height < 1 {
		return nil
	}
	accent, dim, reset := p.Theme.Accent(), terminalui.Dim, terminalui.Reset
	if d.Call == nil {
		label := accent + "? " + reset + fmt.Sprintf("main asks %d questions", d.Count) + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " answer" + reset
		if superseding {
			label = dim + fmt.Sprintf("enter starts a new turn · dismisses %d questions", d.Count) + reset
		}
		padding := min(2, max(0, (width-20)/2))
		return []string{strings.Repeat(" ", padding) + ansi.Truncate(label, width-padding, "…")}
	}
	view := d.Call
	question := view.Questions[d.Index]
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
	if height >= 10 {
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
	if view.Sync {
		keys = append(keys, [2]string{"tab", "note"})
	}
	if total > 1 {
		keys = append(keys, [2]string{"enter", "next"}, [2]string{"←/→", arrows})
	}
	keys = append(keys, [2]string{"ctrl+]", "skip"}, [2]string{"esc", "hide"})
	hints := func(pairs [][2]string) []string { return panelKeys(pairs, inner) }
	foot := hints(keys)
	if len(foot) > max(2, height/3) {
		compact := [][2]string{{"↑↓", "pick"}}
		if total > 1 {
			compact = append(compact, [2]string{"↵", "next"}, [2]string{"←→", arrows})
		}
		foot = hints(append(compact, [2]string{"^]", "skip"}, [2]string{"esc", "hide"}))
	}
	if d.Confirm {
		foot = promptWrap(accent+fmt.Sprintf("Submit with %d unanswered?", d.Gaps)+reset+dim+" · enter submit · esc back"+reset, inner)
	}
	text := promptWrap(textsanitize.Safe(question.Text, false), max(1, inner-2))
	for i, line := range text {
		text[i] = "  " + line
	}
	textRoom := max(1, height-len(rows)-len(foot)-5)
	layout.textTop = min(layout.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[layout.textTop:min(len(text), layout.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, "  "+dim+ansi.Truncate("pgup/pgdn question text", inner-2, "")+reset)
	}
	if height >= 10 {
		rows = append(rows, "")
	}
	options := append(slices.Clone(question.Choices), prompt.Option{Label: "Other", Description: "Type your answer"})
	count := min(8, len(options), max(1, height-len(rows)-len(foot)-2))
	layout.top = min(question.Selected, min(layout.top, max(0, len(options)-count)))
	if question.Selected >= layout.top+count {
		layout.top = question.Selected - count + 1
	}
	columns := 0
	for _, o := range options[layout.top:min(len(options), layout.top+count)] {
		columns = max(columns, ansi.StringWidth(promptText(o.Label)))
	}
	for i := layout.top; i < min(len(options), layout.top+count); i++ {
		o := options[i]
		line := choiceLines(p.Theme, false, i == question.Selected, i+1, promptText(o.Label), math.MaxInt)[0]
		if inner-columns-7 >= 24 {
			line += strings.Repeat(" ", max(0, columns-ansi.StringWidth(promptText(o.Label)))+2) + dim + promptText(o.Description) + reset
		}
		rows = append(rows, ansi.Truncate(line, inner, "…"))
	}
	if count < len(options) {
		rows = append(rows, "  "+dim+ansi.Truncate(fmt.Sprintf("option %d/%d", question.Selected+1, len(options)), inner-2, "")+reset)
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
