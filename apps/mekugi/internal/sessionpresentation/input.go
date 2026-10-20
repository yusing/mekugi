package sessionpresentation

import (
	"errors"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

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
	if m.readOnly() != Editable || control == nil {
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

type EditorIntent struct {
	Version Version
	Target  conversationmodel.EditorTarget
	Action  composer.Action
}

func (v View) EditorTarget() conversationmodel.EditorTarget {
	if p := v.PromptDraft; p.Call != "" || p.Request != "" {
		return conversationmodel.EditorTarget{Call: p.Call, Question: p.Question, Request: p.Request, Revision: p.Revision}
	}
	return conversationmodel.EditorTarget{Revision: v.Draft.Revision}
}

// PasteIntent names a completed paste destination, never its payload.
type PasteIntent struct {
	Version Version
	Target  conversationmodel.EditorTarget
	Menu    uint64
}

func (m *Model) AdmitPaste(in PasteIntent, conversation *conversationmodel.Session) error {
	if in.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.readOnly() != Editable {
		return ErrInputUnavailable
	}
	if in.Target != conversation.EditorTarget() {
		return conversationmodel.ErrDraftChanged
	}
	menu := uint64(0)
	if m.view.Menu != nil {
		menu = m.view.Menu.ID
	}
	if in.Menu != menu {
		return ErrStaleIntent
	}
	return nil
}

type ReferenceIntent struct {
	Version                   Version
	Target                    conversationmodel.EditorTarget
	Description, Text, Source string
}

// Reference admits the input destination and its return to Main together. The
// application then passes the source to Conversation's attachment pipeline.
func (m *Model) Reference(in ReferenceIntent, conversation *conversationmodel.Session) error {
	if in.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	if in.Target != conversation.EditorTarget() {
		return conversationmodel.ErrDraftChanged
	}
	return m.Display(DisplayIntent{Version: in.Version, Thread: m.view.Main})
}

func (m *Model) ApplyEditor(intent EditorIntent, conversation *conversationmodel.Session) (bool, error) {
	if intent.Version != m.view.Version {
		return false, ErrStaleIntent
	}
	if m.readOnly() != Editable {
		return false, ErrInputUnavailable
	}
	return conversation.ApplyEditor(intent.Target, intent.Action)
}

type ComposerOperation uint8

const (
	NoComposerOperation ComposerOperation = iota
	SubmitComposer
	QueueComposer
	CancelComposer
	AttachClipboard
	EditExternally
	RestoreWaiting
	IncreaseEffort
	DecreaseEffort
)

type ComposerIntent struct {
	Version   Version
	Target    conversationmodel.EditorTarget
	Operation ComposerOperation
}

func (m *Model) AdmitComposer(intent ComposerIntent, conversation *conversationmodel.Session) error {
	if intent.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.readOnly() != Editable || intent.Operation < SubmitComposer || intent.Operation > DecreaseEffort {
		return ErrInputUnavailable
	}
	if intent.Target != conversation.EditorTarget() {
		return conversationmodel.ErrDraftChanged
	}
	return nil
}

// AdmitAcquiredInput preserves an already-admitted destination across terminal
// handoff or acquisition. Clock/status publications do not replace that editor.
func (m *Model) AdmitAcquiredInput(intent ComposerIntent, conversation *conversationmodel.Session) error {
	if intent.Operation != AttachClipboard && intent.Operation != EditExternally {
		return ErrInputUnavailable
	}
	if intent.Version.Attachment != m.view.Version.Attachment {
		return ErrStaleIntent
	}
	intent.Version = m.view.Version
	return m.AdmitComposer(intent, conversation)
}
