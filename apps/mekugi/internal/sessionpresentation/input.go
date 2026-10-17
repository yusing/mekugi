package sessionpresentation

import (
	"errors"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

type DraftEditIntent struct {
	Version    Version
	Revision   uint64
	Start, End int
	Text       string
}

type SubmitIntent struct {
	Version  Version
	Revision uint64
	Mode     sessioncontrol.InputMode
}

type InputAdmission struct {
	Edit, Submit bool
	Reason       string
}

func (m *Model) inputAdmission(control *sessioncontrol.Live, ordinary, restoring bool) InputAdmission {
	if m.View().ReadOnly != Editable {
		return InputAdmission{Reason: ErrInputUnavailable.Error()}
	}
	if !ordinary {
		return InputAdmission{Reason: "Finish or hide the active prompt before ordinary input."}
	}
	if restoring || control.Thread == "" || control.Replacement.Archiving() {
		return InputAdmission{Edit: true, Reason: "Keep this draft until the session is ready."}
	}
	return InputAdmission{Edit: true, Submit: true}
}

var ErrInputUnavailable = errors.New("Ordinary input is unavailable for this view.")

func (m *Model) EditDraft(intent DraftEditIntent, conversation *conversationmodel.Session) error {
	if intent.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.View().ReadOnly != Editable {
		return ErrInputUnavailable
	}
	return conversation.EditDraft(intent.Revision, intent.Start, intent.End, intent.Text)
}

func (m *Model) TakeInput(intent SubmitIntent, control *sessioncontrol.Live, conversation *conversationmodel.Session, restoring bool) (composer.Draft, sessioncontrol.QueuePlan, error) {
	if intent.Version != m.view.Version {
		return composer.Draft{}, sessioncontrol.QueuePlan{}, ErrStaleIntent
	}
	if intent.Mode != sessioncontrol.SendInput && intent.Mode != sessioncontrol.QueueNextTurn {
		return composer.Draft{}, sessioncontrol.QueuePlan{}, ErrInputUnavailable
	}
	draft := conversation.DraftView()
	if admission := m.inputAdmission(control, draft.Available, restoring); !admission.Submit {
		return composer.Draft{}, sessioncontrol.QueuePlan{}, errors.New(admission.Reason)
	}
	if draft.Revision != intent.Revision {
		return composer.Draft{}, sessioncontrol.QueuePlan{}, conversationmodel.ErrDraftChanged
	}
	text := strings.TrimSpace(draft.Draft.Text)
	if text == "" || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!") {
		return composer.Draft{}, sessioncontrol.QueuePlan{}, errors.New("Enter ordinary input, or use its command operation.")
	}
	plan := control.PlanInput(intent.Mode, conversation.Questions.Waiting(control.Thread))
	value, err := conversation.TakeDraft(intent.Revision)
	return value, plan, err
}
