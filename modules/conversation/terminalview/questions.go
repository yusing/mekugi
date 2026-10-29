// Source: apps/mekugi/internal/router/app_server_questions.go:619:737@[774b4e24] question dock rendering.
package terminalview

import (
	"fmt"
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

func Questions(d prompt.QuestionDockView, p *terminalui.Painter, layout *QuestionLayout, selection string, superseding bool, width, height int) []string {
	if d.Count == 0 || height < 1 {
		return nil
	}
	accent, dim, reset := p.Theme.Accent(), terminalui.Dim, terminalui.Reset
	if d.Call == nil {
		label := accent + "? " + reset + fmt.Sprintf("main asks %d questions", d.Count) + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " answer" + reset
		if superseding {
			label = dim + fmt.Sprintf("enter starts a new turn · dismisses %d questions", d.Count) + reset
		}
		return []string{ansi.Truncate(label, width, "…")}
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
	head := fmt.Sprintf("? %d of %d", position+1, total)
	if question.Header != question.Text && ansi.StringWidth(head)+ansi.StringWidth(question.Header)+len(state)+6 <= inner {
		head += " · " + promptText(question.Header)
	}
	gap := inner - ansi.StringWidth(head) - len(state)
	header := accent + "\x1b[1m" + head + reset
	if gap >= 2 {
		header += strings.Repeat(" ", gap) + accent + state + reset
	}
	rows := []string{ansi.Truncate(header, inner, "")}
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
	hints := func(pairs [][2]string) []string {
		var parts []string
		for _, pair := range pairs {
			parts = append(parts, accent+pair[0]+reset+dim+" "+pair[1]+reset)
		}
		return promptWrap(strings.Join(parts, dim+" · "+reset), inner)
	}
	foot := hints(keys)
	if len(foot) > max(2, height/3) {
		compact := [][2]string{{"↑↓", "pick"}}
		if total > 1 {
			compact = append(compact, [2]string{"↵", "next"}, [2]string{"←→", arrows})
		}
		foot = hints(append(compact, [2]string{"^]", "skip"}, [2]string{"esc", "hide"}))
	}
	if d.Confirm {
		gaps := d.Gaps
		foot = promptWrap(accent+fmt.Sprintf("Submit with %d unanswered?", gaps)+reset+" · "+accent+"enter"+reset+dim+" submit · "+reset+accent+"esc"+reset+dim+" back"+reset, inner)
	}
	text := promptWrap(textsanitize.Safe(question.Text, false), inner)
	textRoom := max(1, height-len(rows)-len(foot)-5)
	layout.textTop = min(layout.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[layout.textTop:min(len(text), layout.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, dim+ansi.Truncate("pgup/pgdn question text", inner, "")+reset)
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
		prefix := "  "
		if i == question.Selected {
			prefix = "› "
		}
		line := fmt.Sprintf("%s%d. %s", prefix, i+1, promptText(o.Label))
		if inner-columns-7 >= 24 {
			line += strings.Repeat(" ", max(0, columns-ansi.StringWidth(promptText(o.Label)))+2) + dim + promptText(o.Description) + reset
		}
		line = ansi.Truncate(line, inner, "…")
		if i == question.Selected {
			line = selection + ansi.Strip(line) + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + reset
		}
		rows = append(rows, line)
	}
	if count < len(options) {
		rows = append(rows, dim+ansi.Truncate(fmt.Sprintf("option %d/%d", question.Selected+1, len(options)), inner, "")+reset)
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
