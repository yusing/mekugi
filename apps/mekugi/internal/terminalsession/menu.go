package terminalsession

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

type Menu struct {
	body     *sessionpresentation.MenuView
	version  sessionpresentation.Version
	Selected int
	// label is the preset label typed here. Queued keys can arrive before the
	// host echoes the previous one, so each search sends this complete text.
	label string
	terminalui.ChoiceListLayout
	Rect Rect
}
type MenuResult struct {
	Handled, Submit bool
	Action          sessionpresentation.MenuAction
}

func (m *Menu) Update(v sessionpresentation.View) {
	if v.Menu == nil {
		*m = Menu{}
		return
	}
	var previous completion.ChoiceID
	fresh := m.body == nil || m.version.Attachment != v.Version.Attachment || m.body.ID != v.Menu.ID || m.body.Kind != v.Menu.Kind
	commandQueryChanged := !fresh && v.Menu.Kind == sessionpresentation.MenuCompletion && v.Menu.Target.Kind == '/' && m.body.Target.Query != v.Menu.Target.Query
	if !fresh && !commandQueryChanged && m.Selected < len(m.body.Choices) {
		previous = m.body.Choices[m.Selected].Identity()
	}
	if fresh {
		*m = Menu{label: v.Menu.Query}
		previous = v.Menu.Preferred
	}
	m.body, m.version = v.Menu, v.Version
	selected := slices.IndexFunc(m.body.Choices, func(c completion.Choice) bool { return c.Identity() == previous })
	if selected >= 0 {
		m.Selected = selected
	} else if m.body.Kind == sessionpresentation.MenuManageSkills || m.body.Target.Kind == '/' {
		m.Selected = 0
	}
	m.Selected = max(0, min(m.Selected, len(m.body.Choices)-1))
}
func (m *Menu) Open() bool { return m.body != nil }
func (m *Menu) Modal() bool {
	return m.body != nil && m.body.Kind != sessionpresentation.MenuCompletion
}
func (m *Menu) action(kind sessionpresentation.MenuActionKind) sessionpresentation.MenuAction {
	return sessionpresentation.MenuAction{Version: m.version, Menu: m.body.ID, Kind: kind}
}
func (m *Menu) accept(submit bool) MenuResult {
	r := MenuResult{Handled: true}
	if len(m.body.Choices) == 0 {
		return r
	}
	r.Action = m.action(sessionpresentation.MenuAccept)
	r.Action.Choice = m.body.Choices[m.Selected].Identity()
	if m.body.Kind == "preset-label" {
		r.Action.Query = m.label
	}
	r.Submit = submit && m.body.Kind == sessionpresentation.MenuCompletion && m.body.Target.Kind == '/'
	return r
}

// Source: modules/conversation/terminalview/picker_keys.go and router's
// app_server_{archive,copy,settings}.go@932125e0. Navigation is renderer-local.
func (m *Menu) Key(key string) MenuResult {
	if m.body == nil {
		return MenuResult{}
	}
	p := m.body
	if p.Loading && p.Kind != sessionpresentation.MenuCompletion && key != "\x1b" && key != "\x03" {
		return MenuResult{Handled: true}
	}
	if p.Kind == "preset-confirm" {
		if key == "\r" {
			return MenuResult{Handled: true}
		}
		if key == "y" {
			m.Selected = 0
			return m.accept(false)
		}
	}
	if p.Kind == "preset-label" {
		if r, ok := m.labelKey(key); ok {
			return r
		}
	}
	if (p.Kind == "presets" || p.Kind == "preset-slots") && len(key) == 1 {
		for i, choice := range p.Choices {
			if choice.Name == key || key == "r" && choice.Name == "retry" {
				m.Selected = i
				return m.accept(false)
			}
		}
	}
	modal := p.Kind != sessionpresentation.MenuCompletion
	r := MenuResult{Handled: modal}
	move := func(delta int) {
		if n := len(p.Choices); n > 0 {
			m.Selected = (m.Selected + delta + n) % n
		}
		r.Handled = true
	}
	switch key {
	case "\x1b":
		r.Handled = true
		r.Action = m.action(sessionpresentation.MenuDismiss)
	case "\x03":
		if modal {
			r.Action = m.action(sessionpresentation.MenuDismiss)
		}
	case "\x1b[A", "\x1bOA", "\x10":
		move(-1)
	case "\x1b[B", "\x1bOB", "\x0e":
		move(1)
	case "\x1b[5~":
		if modal && p.Kind != sessionpresentation.MenuArchive {
			m.Selected = max(0, m.Selected-8)
		}
	case "\x1b[6~":
		if modal && p.Kind != sessionpresentation.MenuArchive {
			m.Selected = min(max(0, len(p.Choices)-1), m.Selected+8)
		}
	case "\x1b[H", "\x1bOH":
		if modal && p.Kind != sessionpresentation.MenuArchive {
			m.Selected = 0
		}
	case "\x1b[F", "\x1bOF":
		if modal && p.Kind != sessionpresentation.MenuArchive {
			m.Selected = max(0, len(p.Choices)-1)
		}
	case "\t":
		switch p.Kind {
		case sessionpresentation.MenuArchive, sessionpresentation.MenuSideReplace, sessionpresentation.MenuSettings:
			move(1)
		case sessionpresentation.MenuCompletion:
			if len(p.Choices) > 0 {
				return m.accept(false)
			}
			r.Handled = true
			r.Action = m.action(sessionpresentation.MenuDismiss)
		}
	case "\r":
		if p.Kind == sessionpresentation.MenuSettings && p.Loading {
			return r
		}
		if len(p.Choices) > 0 {
			return m.accept(true)
		}
		if !modal {
			r.Action = m.action(sessionpresentation.MenuDismiss)
			r.Handled = p.Target.Kind == '$'
		}
	case " ":
		if p.Kind == sessionpresentation.MenuManageSkills {
			return m.accept(false)
		}
	case "1", "2":
		if p.Kind == sessionpresentation.MenuSkills {
			m.Selected = int(key[0] - '1')
			return m.accept(false)
		}
		if p.Kind == sessionpresentation.MenuManageSkills {
			r.Action = m.action(sessionpresentation.MenuSearch)
			r.Action.Query = p.Query + key
		}
	case "\x7f", "\x08":
		if p.Kind == sessionpresentation.MenuManageSkills && p.Query != "" {
			_, n := utf8.DecodeLastRuneInString(p.Query)
			r.Action = m.action(sessionpresentation.MenuSearch)
			r.Action.Query = p.Query[:len(p.Query)-n]
		}
	default:
		if p.Kind == sessionpresentation.MenuManageSkills && len(key) == 1 && key[0] >= 32 {
			r.Action = m.action(sessionpresentation.MenuSearch)
			r.Action.Query = p.Query + key
		}
	}
	return r
}

// labelKey edits a preset label as a single-line query; Enter saves it.
func (m *Menu) labelKey(key string) (MenuResult, bool) {
	query := m.label
	switch r, n := utf8.DecodeRuneInString(key); {
	case key == "\r":
		return m.accept(false), true
	case key == "\x7f" || key == "\x08":
		_, n := utf8.DecodeLastRuneInString(query)
		query = query[:len(query)-n]
	case key == "\x15":
		query = ""
	case n == len(key) && r != utf8.RuneError && unicode.IsPrint(r):
		query += key
	default:
		return MenuResult{}, false
	}
	r := MenuResult{Handled: true}
	if query != m.label {
		m.label = query
		r.Action = m.action(sessionpresentation.MenuSearch)
		r.Action.Query = query
	}
	return r, true
}
func (m *Menu) Mouse(button, y int) MenuResult {
	if m.body == nil {
		return MenuResult{}
	}
	switch button {
	case 0:
		row := y - m.Rect.Y - m.RowStart
		if row >= 0 && row < m.RowCount && m.Top+row < len(m.body.Choices) {
			m.Selected = m.Top + row
			return m.accept(true)
		}
	case 64:
		return m.Key("\x10")
	case 65:
		return m.Key("\x0e")
	}
	return MenuResult{Handled: true}
}

func (m *Menu) pastePreset(event Input) bool {
	return m.body != nil && strings.HasPrefix(string(m.body.Kind), "preset")
}
