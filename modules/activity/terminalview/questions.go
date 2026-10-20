package terminalview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// Question is a presentation snapshot, never an editable or transport record.
type Question = presentation.Question

func (p *Painter) QuestionState(state string) string {
	color := terminalui.Dim
	switch state {
	case "answered", "answered elsewhere":
		color = "\x1b[38;2;165;214;167m"
		if p.Theme == diffrender.LightTheme {
			color = "\x1b[38;2;10;102;52m"
		}
	case "open", "waiting", "sending":
		color = p.Theme.Accent()
	}
	return color + state + terminalui.Reset
}

func (p *Painter) questionRows(block Block, width int) []string {
	count, state, _ := strings.Cut(block.Label, " · ")
	header := p.Theme.Accent() + "Asked" + terminalui.Reset + "  " + terminalui.Dim + count + terminalui.Undim
	if state != "" {
		header += " · " + p.QuestionState(state)
	}
	rows := terminalui.Wrap(header, width, false)
	inner := max(1, width-2)
	for i, q := range block.Questions {
		if i > 0 {
			rows = append(rows, "")
		}
		text := textsanitize.Safe(q.Text, false)
		rows = append(rows, liveActivityIndent(terminalui.Wrap(text, inner, false), "  ")...)
		if len(block.Questions) > 1 {
			rows = append(rows, "  "+p.QuestionState(q.State))
		}
		switch {
		case q.Answer != "":
			answer := p.Markdown(q.Answer, max(1, width-4), false)
			for j, row := range answer {
				prefix := "    "
				if j == 0 {
					prefix = "  " + p.Theme.Accent() + "└ " + terminalui.Reset
				}
				rows = append(rows, prefix+row)
			}
		case q.Options != "":
			rows = append(rows, liveActivityIndent(terminalui.Wrap(terminalui.Dim+textsanitize.Safe(q.Options, false)+terminalui.Undim, inner, false), "  ")...)
		}
	}
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, width, "")
	}
	return rows
}
