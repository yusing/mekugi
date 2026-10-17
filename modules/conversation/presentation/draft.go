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

// PromptDraftView contains only the active editor, never parked histories or secrets.
type PromptDraftView struct {
	Call, Question, Request string
	Draft                   composer.Draft
	Revision                uint64
}

func (m *Session) PromptDraftView() PromptDraftView {
	var v PromptDraftView
	if call := m.Questions.Active; call != nil {
		v.Call, v.Question = call.ID, call.View().Questions[m.Questions.Index].ID
		if m.Questions.Current().IsSecret {
			return v
		}
	} else if m.Approvals.Open && len(m.Approvals.Pending) > 0 {
		v.Request = m.Approvals.Pending[0].ID
	} else {
		return v
	}
	v.Draft, v.Revision = m.Editor.Draft.Editable(), m.Editor.Revision()
	return v
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

// EditorTarget distinguishes ordinary input from the current non-secret prompt.
type EditorTarget struct {
	Call, Question, Request string
	Revision                uint64
}

func (m *Session) EditorTarget() EditorTarget {
	if m.ordinaryEditor() {
		return EditorTarget{Revision: m.Editor.Revision()}
	}
	view := m.PromptDraftView()
	return EditorTarget{Call: view.Call, Question: view.Question, Request: view.Request, Revision: view.Revision}
}

func (m *Session) ApplyEditor(target EditorTarget, action composer.Action) (bool, error) {
	if target != m.EditorTarget() || m.Questions.Current() != nil && m.Questions.Current().IsSecret {
		return false, ErrDraftChanged
	}
	return m.Editor.Apply(action)
}
