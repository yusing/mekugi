package terminalsession

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

type PromptInput struct {
	Version sessionpresentation.Version
	conversationui.PromptKey
}

// PromptKey interprets the active dock without borrowing an application editor.
func (m *Main) PromptKey(key string, prompts *Prompts) *PromptInput {
	v := m.session
	var result conversationui.PromptKey
	if v.Approvals.Open && v.Approvals.Count > 0 {
		result = prompts.ApprovalLayout(v.Approvals.Approval.ID).Key(v.Approvals.Approval, key, v.PromptDraft.Draft.Text == "")
	} else if v.Questions.Call != nil {
		empty := v.PromptDraft.Draft.Text == ""
		if v.Questions.Call.Questions[v.Questions.Index].Secret {
			empty = prompts.Secret.Text == ""
		}
		result = prompts.QuestionLayout(v.Questions).Key(v.Questions, key, empty)
	} else {
		return nil
	}
	return &PromptInput{Version: v.Version, PromptKey: result}
}

// KeyContinuation is opaque renderer-local state for one decoded key. The
// application settles the preceding effect before returning it to ContinueKey.
type KeyContinuation struct {
	key     string
	phase   uint8
	regular bool
}

const (
	mainKeyPrompt uint8 = iota
	mainKeyPrelude
	mainKeyMenu
	mainKeyComposer
)

type MainKeyInput struct {
	Prompt                  *PromptInput
	Panel                   *sessionpresentation.PanelAction
	Menu                    *MenuResult
	Composer                *ComposerInput
	Next                    *KeyContinuation
	Regular, ClearSelection bool
}

func (m *Main) BeginKey(key string, prompts *Prompts, transcript *activityui.View, focused bool) MainKeyInput {
	return m.ContinueKey(KeyContinuation{key: key}, prompts, transcript, focused)
}

// ContinueKey owns Main's keyboard ordering, including Meta normalization and
// slash-completion continuation. It never applies an application effect.
// Source: apps/mekugi/internal/router/app_server_ui.go:873:935@ac13a4fc.
func (m *Main) ContinueKey(next KeyContinuation, prompts *Prompts, transcript *activityui.View, focused bool) MainKeyInput {
	key := next.key
	if next.phase == mainKeyPrompt {
		next.phase = mainKeyPrelude
		if input := m.PromptKey(key, prompts); input != nil {
			if input.Action == conversationui.PromptCursor {
				next.phase = mainKeyComposer
			}
			return MainKeyInput{Prompt: input, Next: &next}
		}
	}
	if next.phase == mainKeyPrelude {
		next.regular = true
		if m.Panel.Open() {
			action, clear := m.Panel.Key(key)
			return MainKeyInput{Panel: &action, ClearSelection: clear, Regular: true}
		}
		if strings.HasPrefix(key, "\x1b") {
			m.Keybindings = false
		}
		if key == "\x1b\x7f" || key == "\x1b\x08" || key == "\x1bb" || key == "\x1bf" {
			input := m.ComposerKey(key, prompts, transcript)
			return MainKeyInput{Composer: &input, Regular: true}
		}
		next.phase = mainKeyMenu
		if len(key) == 2 && key[0] == 27 && key[1] != '[' && key[1] != 'O' {
			result := m.Menu.Key("\x1b")
			result.Handled = false // Dismissing the menu does not consume its trailing key.
			next.key = key[1:]
			return MainKeyInput{Menu: &result, Next: &next, Regular: true}
		}
	}
	if next.phase == mainKeyMenu {
		next.phase = mainKeyComposer
		compactTab := key == "\t" && strings.TrimSpace(m.session.Draft.Draft.Text) == "/compact" && !m.Menu.Modal()
		if !compactTab {
			result := m.Menu.Key(key)
			return MainKeyInput{Menu: &result, Next: &next, Regular: true}
		}
	}
	if m.session.Questions.Call == nil && !m.session.Approvals.Open && key == "?" && m.session.Draft.Draft.Text == "" && focused {
		m.Keybindings = !m.Keybindings
		return MainKeyInput{Regular: next.regular}
	}
	m.Keybindings = false
	input := m.ComposerKey(key, prompts, transcript)
	return MainKeyInput{Composer: &input, Regular: next.regular}
}
