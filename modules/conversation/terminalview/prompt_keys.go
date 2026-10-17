// Source: apps/mekugi/internal/router/app_server_questions.go:307:394 and app_server_approvals.go:453:500@[774b4e24] prompt keyboard interpretation.
package terminalview

import (
	"slices"
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

// PromptKey contains detached targeted edits and terminal/application actions.
type PromptKey struct {
	Action   PromptAction
	Question prompt.QuestionInput
	Approval prompt.ApprovalInput
}

func (l *QuestionLayout) Key(m prompt.QuestionDockView, key string, empty bool) PromptKey {
	if m.Call == nil {
		return PromptKey{Action: PromptPass}
	}
	q := m.Call.Questions[m.Index]
	result := PromptKey{Action: PromptHandled, Question: prompt.QuestionInput{Call: m.Call.ID, Question: q.ID}}
	edit := &result.Question
	if m.Confirm {
		if key == "\r" {
			result.Action = PromptSubmit
			return result
		}
		if key == "\x1b" {
			edit.Kind = prompt.InputCancelConfirm
			return result
		}
	}
	switch key {
	case "\x1b":
		if q.Note {
			edit.Kind = prompt.InputNote
		} else {
			result.Action = PromptHide
		}
	case "\x1b[A", "\x10", "\x1b[B", "\x0e":
		step := 1
		if key == "\x1b[A" || key == "\x10" {
			step = -1
		}
		edit.Kind, edit.Choice = prompt.InputChoose, (q.Selected+step+len(q.Choices)+1)%(len(q.Choices)+1)
	case "\x1b[D", "\x1b[C":
		if q.Note || q.Selected == len(q.Choices) {
			result.Action = PromptCursor
		} else if key == "\x1b[D" {
			result.Action = PromptPrevious
		} else {
			result.Action = PromptNext
		}
	case "\x1b[5~":
		l.textTop = max(0, l.textTop-1)
	case "\x1b[6~":
		l.textTop++
	case "\t":
		if m.Call.Sync && q.Selected < len(q.Choices) {
			edit.Kind, edit.Enabled = prompt.InputNote, true
		}
	case "\x16":
		edit.Kind, edit.Enabled, result.Action = prompt.InputEdit, !q.Note, PromptAttach
	case "\r":
		result.Action = PromptAnswer
	case "\x1d":
		result.Action = PromptSkip
	default:
		if !m.Call.Local && len(key) == 1 && key[0] >= '1' && key[0] <= '9' && !q.Note && empty {
			if selected := int(key[0] - '1'); selected <= len(q.Choices) {
				edit.Kind, edit.Choice = prompt.InputChoose, selected
				return result
			}
		}
		if len(key) == 1 {
			edit.Kind, edit.Enabled = prompt.InputEdit, key[0] >= 32 && key[0] != 127 && !q.Note
		}
		result.Action = PromptPass
	}
	return result
}

func (l *ApprovalLayout) Key(a prompt.ApprovalView, key string, empty bool) PromptKey {
	result := PromptKey{Action: PromptHandled, Approval: prompt.ApprovalInput{Request: a.ID}}
	deny := slices.IndexFunc(a.Choices, func(c prompt.Choice) bool { return c.Deny })
	selected := -1
	switch key {
	case "\x1b":
		result.Action = PromptHide
	case "\x1b[A", "\x10", "\x1b[B", "\x0e":
		if len(a.Choices) > 0 {
			step := 1
			if key == "\x1b[A" || key == "\x10" {
				step = -1
			}
			selected = (a.Selected + step + len(a.Choices)) % len(a.Choices)
		}
	case "\x1b[5~":
		l.textTop = max(0, l.textTop-1)
	case "\x1b[6~":
		l.textTop++
	case "\r":
		if len(a.Choices) > 0 {
			result.Action = PromptSubmit
		}
	case "\t":
		selected = deny
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' && (deny < 0 || empty) {
			if index := int(key[0] - '1'); index < len(a.Choices) {
				selected = index
			}
			break
		}
		if deny >= 0 {
			if len(key) == 1 && key[0] >= 32 && key[0] != 127 {
				selected, result.Action = deny, PromptPass
				break
			}
			switch key {
			case "\x7f", "\x08", "\x17", "\x0b", "\x1a", "\x19", "\n", "\x1b[D", "\x1b[C", "\x1b[H", "\x1b[F", "\x1b[3~":
				result.Action = PromptPass
				return result
			}
		}
		// Preserve pane commands and interruption without editing hidden Main.
		if len(key) == 1 && key != "\x03" && key != "\x02" || key == "\x1b[3~" {
			break
		}
		result.Action = PromptPass
	}
	if selected >= 0 {
		result.Approval.Select, result.Approval.Choice = true, a.Choices[selected].ID
	}
	return result
}

func promptWrap(text string, width int) []string {
	return strings.Split(ansi.Wrap(text, max(1, width), ""), "\n")
}

func promptText(text string) string {
	return strings.ReplaceAll(textsanitize.Safe(text, false), "\n", " ")
}
