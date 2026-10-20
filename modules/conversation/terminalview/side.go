// Source: apps/mekugi/internal/router/app_server_btw.go:397:445@[4ded0a4e] side-answer dock painting.
package terminalview

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

type SideFrame struct {
	Rows, CopyAnswer   []string
	AnswerRows, Scroll int
}

// status is renderer-owned text, optionally decorated by the shared animation.
func Side(view conversationmodel.SideView, painter *terminalui.Painter, status string, width, height, scroll int) SideFrame {
	frame := SideFrame{Scroll: scroll}
	if height < 1 {
		return frame
	}
	color := painter.Theme.Accent()
	if view.Alert {
		color = terminalui.Red
	}
	label := " /btw · " + status
	rows := []string{ansi.Truncate(color+"╭─"+label+terminalui.Reset, width, "…")}
	inner := max(1, width-2)
	var answer []string
	var scope uint64
	exchange := func(question, notice, failure string, items []conversationmodel.Answer) {
		if len(answer) > 0 {
			answer = append(answer, "")
		}
		answer = append(answer, terminalui.Wrap(textsanitize.Safe(question, false), inner, true)...)
		if notice != "" {
			for _, row := range terminalui.Wrap(textsanitize.Safe(notice, false), inner, true) {
				answer = append(answer, terminalui.Red+row+terminalui.Reset)
			}
		}
		for _, item := range items {
			scope++
			painter.CopyScope = scope
			answer = append(answer, painter.Markdown(item.Text, inner, false)...)
		}
		if failure != "" {
			for _, row := range terminalui.Wrap(textsanitize.Safe(failure, false), inner, true) {
				answer = append(answer, terminalui.Red+row+terminalui.Reset)
			}
		}
	}
	for _, previous := range view.History {
		exchange(previous.Question, previous.AttachmentNotice, previous.Failure, previous.Answer)
	}
	exchange(view.Question, view.AttachmentNotice, "", view.Answer)
	frame.CopyAnswer = answer
	frame.AnswerRows = max(0, height-len(rows)-1)
	frame.Scroll = min(frame.Scroll, max(0, len(answer)-frame.AnswerRows))
	end := len(answer) - frame.Scroll
	for _, row := range answer[max(0, end-frame.AnswerRows):end] {
		rows = append(rows, ansi.Truncate(color+"│ "+terminalui.Reset+row, width, ""))
	}
	hint := "╰─ /btw follow-up · Esc close"
	if view.Status == conversationmodel.SideCompactionRequired {
		hint = "╰─ Esc close · /compact Main, then retry /btw"
	}
	if len(answer) > frame.AnswerRows {
		hint += " · PgUp/PgDn scroll"
	}
	if view.Truncated {
		hint = "╰─ Display truncated · Esc close"
	}
	rows = append(rows, ansi.Truncate(terminalui.Dim+hint+terminalui.Reset, width, "…"))
	frame.Rows = rows[:min(len(rows), height)]
	return frame
}
