package presentation

import (
	"errors"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

var ErrDraftChanged = errors.New("The draft changed; review it before submitting.")

type DraftView struct {
	Draft     composer.Draft
	Revision  uint64
	Available bool
}

func (m *Session) ordinaryEditor() bool { return m.Questions.Active == nil && !m.Approvals.Open }

func (m *Session) DraftView() DraftView {
	if !m.ordinaryEditor() {
		return DraftView{}
	}
	return DraftView{Draft: m.Editor.Draft.Editable(), Revision: m.Editor.Revision(), Available: true}
}

func (m *Session) EditDraft(revision uint64, start, end int, text string) error {
	if !m.ordinaryEditor() || revision != m.Editor.Revision() {
		return ErrDraftChanged
	}
	return m.Editor.Edit(start, end, text)
}

func (m *Session) TakeDraft(revision uint64) (composer.Draft, error) {
	if !m.ordinaryEditor() || revision != m.Editor.Revision() {
		return composer.Draft{}, ErrDraftChanged
	}
	return m.Editor.Take(), nil
}
