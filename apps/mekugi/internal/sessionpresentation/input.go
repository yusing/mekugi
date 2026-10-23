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
	ReadOnly     bool
	Reason       string
}

func (m *Model) inputAdmission(control *sessioncontrol.Live, ordinary, restoring bool) InputAdmission {
	if m.childRuntime || m.readOnly() != Editable {
		return InputAdmission{ReadOnly: true, Reason: ErrInputUnavailable.Error()}
	}
	return ordinaryInputAdmission(control, ordinary, restoring)
}

func ordinaryInputAdmission(control *sessioncontrol.Live, ordinary, restoring bool) InputAdmission {
	if control == nil {
		return InputAdmission{ReadOnly: true, Reason: ErrInputUnavailable.Error()}
	}
	if !ordinary {
		return InputAdmission{Reason: "Finish or hide the active prompt before ordinary input."}
	}
	if restoring || control.Thread == "" || control.Replacement.Archiving() {
		return InputAdmission{Edit: true, Reason: "Keep this draft until the session is ready."}
	}
	return InputAdmission{Edit: true, Submit: true}
}

// AdmitBoundInput validates the owner named by an already captured submission.
// Navigation changes the visible editor, not ownership of its pending input.
func (m *Model) AdmitBoundInput(intent ComposerIntent) error {
	if intent.Thread == "" {
		return m.AdmitClientComposer(intent)
	}
	if err := m.admitOwner(intent.Version); err != nil {
		return err
	}
	if intent.Thread != m.view.Main.ID && intent.Thread != m.view.SideThread.ID {
		return ErrStaleIntent
	}
	if m.childRuntime || m.replay || intent.Operation != SubmitComposer && intent.Operation != QueueComposer {
		return ErrInputUnavailable
	}
	return nil
}

var ErrInputUnavailable = errors.New("Ordinary input is unavailable for this view.")

func (m *Model) TakeInput(intent SubmitIntent, control *sessioncontrol.Live, conversation *conversationmodel.Session, restoring bool) (composer.Draft, sessioncontrol.QueuePlan, error) {
	conversation = m.InputConversation(conversation)
	if m.AdmitDestination(intent.Version) != nil {
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
	Thread  string
	Version Version
	Target  conversationmodel.EditorTarget
	Menu    uint64
}

func (m *Model) AdmitPaste(in PasteIntent, conversation *conversationmodel.Session) error {
	if in.Thread != "" && in.Thread != m.view.Input.ID || in.Thread == "" && m.DisplayingSide() {
		return ErrStaleIntent
	}
	conversation = m.InputConversation(conversation)
	if !in.Version.SameAttachment(m.view.Version) {
		return ErrStaleIntent
	}
	if m.editorUnavailable(in.Target) {
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
	Thread                    string `json:",omitzero"`
	Displayed                 Thread
	Target                    conversationmodel.EditorTarget
	Description, Text, Source string
}

// Reference admits the input destination and its return to Main together. The
// application then passes the source to Conversation's attachment pipeline.
func (m *Model) Reference(in ReferenceIntent, conversation *conversationmodel.Session) error {
	thread := in.Thread
	if thread == "" {
		thread = m.view.Input.ID
	}
	conversation = m.inputConversation(thread, conversation)
	if in.Target != conversation.EditorTarget() {
		return conversationmodel.ErrDraftChanged
	}
	return m.ClientReference(in)
}

// ClientReference admits the destination using the captured session view. The
// eventual insertion is guarded against the client's own editor revision.
func (m *Model) ClientReference(in ReferenceIntent) error {
	if m.admitOwner(in.Version) != nil {
		return ErrStaleIntent
	}
	thread := in.Thread
	if thread == "" {
		if m.AdmitDestination(in.Version) != nil {
			return ErrStaleIntent
		}
		thread = m.view.Input.ID
	}
	if thread != m.view.Main.ID && thread != m.view.SideThread.ID {
		return ErrInputUnavailable
	}
	if m.replay {
		return ErrReplayIntent
	}
	if thread == m.view.SideThread.ID || in.Displayed.ID != m.view.Displayed.ID {
		return nil
	}
	return m.Display(DisplayIntent{Version: m.view.Version, Thread: m.view.Main})
}

func (m *Model) ApplyEditor(intent EditorIntent, conversation *conversationmodel.Session) (bool, error) {
	conversation = m.InputConversation(conversation)
	if m.AdmitDestination(intent.Version) != nil {
		return false, ErrStaleIntent
	}
	if m.editorUnavailable(intent.Target) {
		return false, ErrInputUnavailable
	}
	return conversation.ApplyEditor(intent.Target, intent.Action)
}

func (m *Model) editorUnavailable(target conversationmodel.EditorTarget) bool {
	return m.readOnly() != Editable || m.childRuntime && target.Call == "" && target.Request == ""
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
	AcceptPrediction
	SteerWaiting
)

// ClientLocal identifies operations that acquire resources or edit on the native renderer.
func (o ComposerOperation) ClientLocal() bool {
	return o == CancelComposer || o == AcceptPrediction || o == EditExternally || o == AttachClipboard
}

type ComposerIntent struct {
	Thread    string
	Version   Version
	Target    conversationmodel.EditorTarget
	Operation ComposerOperation
	Work      *sessioncontrol.InterruptTarget `json:",omitzero"`
}

func (m *Model) AdmitComposer(intent ComposerIntent, conversation *conversationmodel.Session) error {
	if err := m.AdmitClientComposer(intent); err != nil {
		return err
	}
	thread := intent.Thread
	if thread == "" {
		thread = m.view.Input.ID
	}
	conversation = m.inputConversation(thread, conversation)
	if intent.Target != conversation.EditorTarget() {
		return conversationmodel.ErrDraftChanged
	}
	return nil
}

// AdmitClientComposer admits a detached client editor request. Its revision is
// local to that client and must never be compared to another editor's revision.
func (m *Model) AdmitClientComposer(intent ComposerIntent) error {
	if intent.Thread != "" && intent.Thread != m.view.Main.ID && intent.Thread != m.view.SideThread.ID {
		return ErrStaleIntent
	}
	if m.admitOwner(intent.Version) != nil {
		return ErrStaleIntent
	}
	if intent.Thread == "" && m.AdmitDestination(intent.Version) != nil {
		return ErrStaleIntent
	}
	// These aggregate in-process controls have no independent work/prediction
	// identity. Native clients resolve them locally into identified actions.
	if (intent.Operation == CancelComposer || intent.Operation == AcceptPrediction) && intent.Version != m.view.Version {
		return ErrStaleIntent
	}
	if (intent.Operation == RestoreWaiting || intent.Operation == SteerWaiting) && intent.Work == nil {
		return ErrStaleIntent
	}
	if m.replay || m.childRuntime && intent.Target.Call == "" && intent.Target.Request == "" || intent.Thread == "" && m.readOnly() != Editable || intent.Operation < SubmitComposer || intent.Operation > SteerWaiting {
		return ErrInputUnavailable
	}
	return nil
}

// ClientInput plans submission without taking or retaining a client's editor.
func (m *Model) ClientInput(intent ComposerIntent, control *sessioncontrol.Live, conversation *conversationmodel.Session, restoring bool) (sessioncontrol.QueuePlan, error) {
	if err := m.AdmitBoundInput(intent); err != nil {
		return sessioncontrol.QueuePlan{}, err
	}
	if intent.Operation != SubmitComposer && intent.Operation != QueueComposer {
		return sessioncontrol.QueuePlan{}, ErrInputUnavailable
	}
	ordinary := intent.Target.Call == "" && intent.Target.Request == ""
	admission := m.inputAdmission(control, ordinary, restoring)
	if intent.Thread != "" && intent.Thread == m.view.Main.ID {
		if control == nil || control.Thread != intent.Thread {
			return sessioncontrol.QueuePlan{}, ErrStaleIntent
		}
		admission = ordinaryInputAdmission(control, ordinary, restoring)
	}
	if !admission.Submit {
		return sessioncontrol.QueuePlan{}, errors.New(admission.Reason)
	}
	mode := sessioncontrol.SendInput
	if intent.Operation == QueueComposer {
		mode = sessioncontrol.QueueNextTurn
	}
	return control.PlanInput(mode, conversation.Questions.Waiting(control.Thread)), nil
}

// AdmitAcquiredInput preserves an already-admitted destination across terminal
// handoff or acquisition. Clock/status publications do not replace that editor.
func (m *Model) AdmitAcquiredInput(intent ComposerIntent, conversation *conversationmodel.Session) error {
	if intent.Operation != AttachClipboard && intent.Operation != EditExternally {
		return ErrInputUnavailable
	}
	if !intent.Version.SameAttachment(m.view.Version) {
		return ErrStaleIntent
	}
	return m.AdmitComposer(intent, conversation)
}
