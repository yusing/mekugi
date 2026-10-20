package sessionpresentation

import "github.com/yusing/mekugi/modules/conversation/completion"

type MenuKind string

const (
	MenuCompletion   MenuKind = ""
	MenuSkills       MenuKind = "menu"
	MenuManageSkills MenuKind = "manage"
	MenuSettings     MenuKind = "settings"
	MenuArchive      MenuKind = "archive"
	MenuRewind       MenuKind = "rewind"
	MenuCopy         MenuKind = "copy"
)

type MenuView struct {
	ID   uint64
	Kind MenuKind
	completion.View
	Preferred            completion.ChoiceID
	Saving, QueueCompact bool
	Setting, Explanation string
	Title                string
	Dialog               bool
}
type MenuActionKind uint8

const (
	MenuNoAction MenuActionKind = iota
	MenuDismiss
	MenuAccept
	MenuSearch
)

type MenuAction struct {
	Version Version
	Menu    uint64
	Kind    MenuActionKind
	Choice  completion.ChoiceID
	Query   string
}

func (m *Model) AdmitMenu(in MenuAction) error {
	p := m.view.Menu
	if in.Version != m.view.Version || p == nil || in.Menu != p.ID {
		return ErrStaleIntent
	}
	if m.replay && in.Kind != MenuDismiss && p.Kind != MenuCopy {
		return ErrReplayIntent
	}
	switch in.Kind {
	case MenuAccept:
		if p.Kind == MenuCompletion && (m.readOnly() != Editable || !m.view.Draft.Available) {
			return ErrInputUnavailable
		}
		for _, choice := range p.Choices {
			if choice.Identity() == in.Choice {
				return nil
			}
		}
		return ErrStaleIntent
	case MenuSearch:
		if p.Kind != MenuManageSkills {
			return ErrStaleIntent
		}
	}
	return nil
}

// MenuState identifies the current application menu; Conversation owns its choices.
// Source: apps/mekugi/internal/router/composer_menu.go:12:16@0ce86dd5.
type MenuState struct {
	Setting   string
	ID        uint64
	Kind      MenuKind
	Preferred completion.ChoiceID
}

func (m *Model) Menu() *MenuState { return &m.menu }

func (m *MenuState) Begin(kind MenuKind) {
	m.ID++
	m.Kind, m.Preferred = kind, completion.ChoiceID{}
}
