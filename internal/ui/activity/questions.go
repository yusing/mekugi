package activity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// Question is a presentation snapshot, never an editable or transport record.
type Question struct {
	Text, Answer, Options, State string
}

func (p *Painter) QuestionState(state string) string {
	color := Dim
	switch state {
	case "answered", "answered elsewhere":
		color = "\x1b[38;2;165;214;167m"
		if p.Theme == livediff.LightTheme {
			color = "\x1b[38;2;10;102;52m"
		}
	case "open", "waiting", "sending":
		color = p.Theme.Accent()
	}
	return color + state + Reset
}

func (p *Painter) questionRows(block Block, width int) []string {
	count, state, _ := strings.Cut(block.Label, " · ")
	header := p.Theme.Accent() + "Asked" + Reset + "  " + Dim + count + Undim
	if state != "" {
		header += " · " + p.QuestionState(state)
	}
	rows := Wrap(header, width, false)
	inner := max(1, width-2)
	for i, q := range block.Questions {
		if i > 0 {
			rows = append(rows, "")
		}
		text := livediff.Safe(q.Text, false)
		rows = append(rows, liveActivityIndent(Wrap(text, inner, false), "  ")...)
		if len(block.Questions) > 1 {
			rows = append(rows, "  "+p.QuestionState(q.State))
		}
		switch {
		case q.Answer != "":
			answer := livediff.Safe(q.Answer, false)
			rows = append(rows, liveActivityHang("  "+p.Theme.Accent()+"└ "+Reset, "\x1b[1m"+answer+Reset, width)...)
		case q.Options != "":
			rows = append(rows, liveActivityIndent(Wrap(Dim+livediff.Safe(q.Options, false)+Undim, inner, false), "  ")...)
		}
	}
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, width, "")
	}
	return rows
}
