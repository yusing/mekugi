// Source: apps/mekugi/internal/router/app_server_approvals.go:502:584@[774b4e24] approval dock rendering.
package terminalview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

type ApprovalLayout struct {
	Column  *int
	textTop int
}

func Approvals(d prompt.ApprovalDockView, p *terminalui.Painter, layout *ApprovalLayout, selection, agent string, width, height int) []string {
	if d.Count == 0 || height < 1 {
		return nil
	}
	accent, dim, reset := p.Theme.Accent(), terminalui.Dim, terminalui.Reset
	if !d.Open {
		label := accent + "! " + reset + fmt.Sprintf("%d approval", d.Count)
		if d.Count > 1 {
			label += "s"
		}
		label += " pending" + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " review" + reset
		return []string{ansi.Truncate(label, width, "…")}
	}
	v := d.Approval
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	head := "! " + v.Title
	state := agent
	if d.Count > 1 {
		state = strings.TrimPrefix(state+fmt.Sprintf(" · 1 of %d", d.Count), " · ")
	}
	header := accent + "\x1b[1m" + head + reset
	if gap := inner - ansi.StringWidth(head) - ansi.StringWidth(state); state != "" && gap >= 2 {
		header += strings.Repeat(" ", gap) + dim + state + reset
	}
	rows := []string{ansi.Truncate(header, inner, "")}
	foot := promptWrap(accent+fmt.Sprintf("1–%d", len(v.Choices))+reset+dim+" choose · "+reset+accent+"enter"+reset+dim+" confirm · "+reset+accent+"esc"+reset+dim+" hide"+reset, inner)
	if d.Reason {
		foot = promptWrap(accent+fmt.Sprintf("1–%d", len(v.Choices))+reset+dim+" choose · "+reset+accent+"type"+reset+dim+" deny with reason · "+reset+accent+"enter"+reset+dim+" confirm · "+reset+accent+"esc"+reset+dim+" hide"+reset, inner)
	}
	var text []string
	for line := range strings.SplitSeq(v.Subject, "\n") {
		text = append(text, promptWrap(textsanitize.Safe(line, false), inner)...)
	}
	for _, detail := range v.Details {
		for _, line := range promptWrap(textsanitize.Safe(detail, false), inner) {
			text = append(text, dim+line+reset)
		}
	}
	// The request text takes precedence over spacing; it scrolls only when
	// even the unspaced dock cannot hold it.
	fixed := len(rows) + len(v.Choices) + len(foot)
	spaced := fixed+len(text)+3 <= height
	textRoom := height - fixed
	if spaced {
		textRoom -= 3
		rows = append(rows, "")
	}
	if len(text) > textRoom {
		textRoom--
	}
	textRoom = max(1, textRoom)
	layout.textTop = min(layout.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[layout.textTop:min(len(text), layout.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, dim+ansi.Truncate("pgup/pgdn request text", inner, "")+reset)
	}
	if spaced {
		rows = append(rows, "")
	}
	for i, choice := range v.Choices {
		prefix := "  "
		if i == v.Selected {
			prefix = "› "
		}
		line := ansi.Truncate(fmt.Sprintf("%s%d. %s", prefix, i+1, promptText(choice.Label)), inner, "…")
		if i == v.Selected {
			line = selection + ansi.Strip(line) + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + reset
		}
		rows = append(rows, line)
	}
	if spaced {
		rows = append(rows, "")
	}
	rows = append(rows, foot...)
	for i := range rows {
		rows[i] = strings.Repeat(" ", padding) + rows[i]
	}
	return rows[:min(len(rows), height)]
}
