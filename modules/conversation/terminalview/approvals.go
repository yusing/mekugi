// Source: apps/mekugi/internal/router/app_server_approvals.go:502:584@[774b4e24] approval dock rendering.
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

type ApprovalLayout struct {
	Column  *int
	textTop int
}

// Approvals docks the open approval as a panel: an accent heading naming the
// request, its text, numbered choices with a pointer on the selected one, and
// dim keys. A hidden approval collapses to one reminder row.
func Approvals(d prompt.ApprovalDockView, p *terminalui.Painter, layout *ApprovalLayout, agent string, width, height int) []string {
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
		padding := min(2, max(0, (width-20)/2))
		return []string{strings.Repeat(" ", padding) + ansi.Truncate(label, width-padding, "…")}
	}
	v := d.Approval
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	state := agent
	if d.Count > 1 {
		state = strings.TrimPrefix(state+fmt.Sprintf(" · 1 of %d", d.Count), " · ")
	}
	if state != "" {
		state = dim + state + reset
	}
	rows := []string{terminalui.PanelHeading(accent, promptText(v.Title), "", state, inner)}
	keys := [][2]string{{fmt.Sprintf("1–%d", len(v.Choices)), "choose"}, {"enter", "confirm"}, {"esc", "hide"}}
	if d.Reason {
		keys = slices.Insert(keys, 1, [2]string{"type", "deny with reason"})
	}
	foot := panelKeys(keys, inner)
	var text []string
	for line := range strings.SplitSeq(v.Subject, "\n") {
		text = append(text, promptWrap(textsanitize.Safe(line, false), max(1, inner-2))...)
	}
	for _, detail := range v.Details {
		for _, line := range promptWrap(textsanitize.Safe(detail, false), max(1, inner-2)) {
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
	for _, line := range text[layout.textTop:min(len(text), layout.textTop+textRoom)] {
		rows = append(rows, "  "+line)
	}
	if len(text) > textRoom {
		rows = append(rows, "  "+dim+ansi.Truncate("pgup/pgdn request text", inner-2, "")+reset)
	}
	if spaced {
		rows = append(rows, "")
	}
	for i, choice := range v.Choices {
		rows = append(rows, ansi.Truncate(choiceLine(accent, choice.Tone, i == v.Selected, i+1, promptText(choice.Label)), inner, "…"))
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

// choiceLine numbers a docked panel's choice behind a pointer on the selected
// one. Toned choices keep their color so risky ones stand apart; a neutral
// choice takes the accent while selected.
func choiceLine(accent string, tone prompt.ChoiceTone, selected bool, number int, label string) string {
	pointer, color := "  ", ""
	if selected {
		pointer, color = accent+"❯"+terminalui.Reset+" ", accent
	}
	switch tone {
	case prompt.ToneAllow:
		color = terminalui.Green
	case prompt.ToneGrant:
		color = terminalui.Amber
	case prompt.ToneRefuse:
		color = terminalui.Red
	}
	line := fmt.Sprintf("%d. %s", number, label)
	if color != "" {
		line = color + line + terminalui.Reset
	}
	return pointer + line
}

// panelKeys lists a docked panel's keys as one dim row, wrapping when narrow.
func panelKeys(keys [][2]string, width int) []string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key[0] + " " + key[1]
	}
	rows := promptWrap(strings.Join(parts, " · "), width)
	for i, row := range rows {
		rows[i] = terminalui.Dim + row + terminalui.Reset
	}
	return rows
}
