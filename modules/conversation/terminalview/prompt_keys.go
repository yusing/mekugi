// Source: apps/mekugi/internal/router/app_server_questions.go:307:394 and app_server_approvals.go:453:500@[774b4e24] prompt keyboard interpretation.
package terminalview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// PromptAction names effects requiring the attached application's editor or
// live request. Key interpretation never sees answer text or response handles.
type PromptAction uint8

const (
	PromptPass PromptAction = iota
	PromptHandled
	PromptHide
	PromptCursor
	PromptPrevious
	PromptNext
	PromptAttach
	PromptAnswer
	PromptSkip
	PromptSubmit
)

func (l *QuestionLayout) Key(m *prompt.Questions, key string, empty bool) PromptAction {
	q := m.Current()
	if q == nil {
		return PromptPass
	}
	if m.Confirm {
		if key == "\r" {
			return PromptSubmit
		}
		if key == "\x1b" {
			m.Confirm = false
			return PromptHandled
		}
	}
	switch key {
	case "\x1b":
		if q.Note {
			q.Note = false
		} else {
			m.AutoOpen = false
			return PromptHide
		}
	case "\x1b[A", "\x10", "\x1b[B", "\x0e":
		step := 1
		if key == "\x1b[A" || key == "\x10" {
			step = -1
		}
		q.Choose((q.Selected + step + len(q.Choices) + 1) % (len(q.Choices) + 1))
	case "\x1b[D", "\x1b[C":
		if q.Note || q.Selected == len(q.Choices) {
			return PromptCursor
		}
		if key == "\x1b[D" {
			return PromptPrevious
		}
		return PromptNext
	case "\x1b[5~":
		l.textTop = max(0, l.textTop-1)
	case "\x1b[6~":
		l.textTop++
	case "\t":
		if m.Active.Sync && q.Selected < len(q.Choices) {
			q.Note = true
		}
	case "\x16":
		q.Edit(!q.Note)
		return PromptAttach
	case "\r":
		return PromptAnswer
	case "\x1d":
		return PromptSkip
	default:
		if !m.Active.Local && len(key) == 1 && key[0] >= '1' && key[0] <= '9' && !q.Note && empty {
			if selected := int(key[0] - '1'); selected <= len(q.Choices) {
				q.Choose(selected)
				return PromptHandled
			}
		}
		if len(key) == 1 {
			q.Edit(key[0] >= 32 && key[0] != 127 && !q.Note)
		}
		return PromptPass
	}
	return PromptHandled
}

func (l *ApprovalLayout) Key(a *prompt.Approval, key string, empty bool) PromptAction {
	switch key {
	case "\x1b":
		return PromptHide
	case "\x1b[A", "\x10":
		a.Move(-1)
	case "\x1b[B", "\x0e":
		a.Move(1)
	case "\x1b[5~":
		l.textTop = max(0, l.textTop-1)
	case "\x1b[6~":
		l.textTop++
	case "\r":
		return PromptSubmit
	case "\t":
		a.SelectDenial()
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' && (a.DenialChoice() < 0 || empty) {
			if index := int(key[0] - '1'); index < len(a.Choices) {
				a.Selected = index
			}
			return PromptHandled
		}
		if deny := a.DenialChoice(); deny >= 0 {
			if len(key) == 1 && key[0] >= 32 && key[0] != 127 {
				a.Selected = deny
				return PromptPass
			}
			switch key {
			case "\x7f", "\x08", "\x17", "\x0b", "\x1a", "\x19", "\n", "\x1b[D", "\x1b[C", "\x1b[H", "\x1b[F", "\x1b[3~":
				return PromptPass
			}
		}
		// Preserve pane commands and interruption without editing hidden Main.
		if len(key) == 1 && key != "\x03" && key != "\x02" || key == "\x1b[3~" {
			return PromptHandled
		}
		return PromptPass
	}
	return PromptHandled
}

func promptWrap(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func promptText(text string) string {
	return strings.ReplaceAll(textsanitize.Safe(text, false), "\n", " ")
}
