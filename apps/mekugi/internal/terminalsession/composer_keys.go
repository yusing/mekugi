package terminalsession

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// editKey maps editing keys to source-coordinate actions without keeping another editor.
// Source: apps/mekugi/internal/router/app_server_ui.go:904:987,1020:1024,
// app_server_composer.go:15:31,55:64@b1ffee83.
func (m *Main) editKey(draft composer.Draft, key string) (composer.Action, bool) {
	action := composer.Action{}
	at := draft.Cursor()
	switch key {
	case "\x1b\x7f", "\x1b\x08", "\x1b[127;3u", "\x1b[8;3u", "\x17", "\x1b[3;3~":
		other := draft.WordBoundary(at, key != "\x1b[3;3~")
		action = composer.Action{Kind: composer.DeleteAction, Start: min(at, other), End: max(at, other)}
	case "\x7f", "\x08":
		action = composer.Action{Kind: composer.DeleteAction, Start: draft.Boundary(at, -1), End: at, Run: composer.BackspaceRun}
	case "\x1b[3~":
		action = composer.Action{Kind: composer.DeleteAction, Start: at, End: draft.Boundary(at, 1), Run: composer.DeleteRun}
	case "\x0b":
		end := len(draft.Text)
		if next := strings.IndexByte(draft.Text[at:], '\n'); next >= 0 {
			end = at + max(1, next)
		}
		for _, span := range draft.Spans() {
			if end > span.Start && end < span.End {
				end = span.End
			}
		}
		action = composer.Action{Kind: composer.DeleteAction, Start: at, End: end}
	case "\x1a":
		action.Kind = composer.UndoAction
	case "\x19", "\x1b[122;6u":
		action.Kind = composer.RedoAction
	case "\n":
		action = composer.Action{Kind: composer.InsertAction, Text: "\n"}
	default:
		if strings.HasPrefix(key, "\x1b") {
			position, history := m.moveDraft(draft, key)
			action = composer.Action{Kind: composer.MoveAction, Start: position}
			if history < 0 {
				action.Kind = composer.RecallPreviousAction
			}
			if history > 0 {
				action.Kind = composer.RecallNextAction
			}
			return action, true
		}
		if len(key) == 0 || key[0] < 32 {
			return action, false
		}
		action = composer.Action{Kind: composer.InsertAction, Text: key}
	}
	m.CursorColumn = nil
	return action, true
}

// ComposerInput carries one targeted application operation or editor action.
// Local secret editing returns only its outcome, never its text.
type ComposerInput struct {
	Editor    sessionpresentation.EditorIntent
	Command   sessionpresentation.ComposerIntent
	LocalEdit composer.ActionKind
	Changed   bool
	Err       error
}

func (m *Main) Reference(description, text, source string) sessionpresentation.ReferenceIntent {
	return sessionpresentation.ReferenceIntent{Version: m.session.Version, Thread: m.session.Input.ID, Displayed: m.session.Displayed, Target: m.session.EditorTarget(), Description: description, Text: text, Source: source}
}

// ComposerKey interprets ordinary input after prompt and menu interception.
// Source: apps/mekugi/internal/router/app_server_ui.go:934:1091@b1ffee83.
func (m *Main) ComposerKey(key string, prompts *Prompts, transcript *activityui.View) ComposerInput {
	view := m.session
	draft := view.Draft.Draft
	target := view.EditorTarget()
	if prompt := view.PromptDraft; prompt.Call != "" || prompt.Request != "" {
		draft = prompt.Draft
	}
	var operation sessionpresentation.ComposerOperation
	switch key {
	case "\x1b[1;2A":
		operation = sessionpresentation.IncreaseEffort
	case "\x1b[1;2B":
		operation = sessionpresentation.DecreaseEffort
	case "\x1b[1;3A", "\x1b[1;2D":
		operation = sessionpresentation.RestoreWaiting
	case "\x1b[5~":
		transcript.ScrollKey('b')
		return ComposerInput{}
	case "\x1b[6~":
		transcript.ScrollKey(' ')
		return ComposerInput{}
	case "\x05":
		transcript.ToggleExpansion()
		return ComposerInput{}
	case "\x07":
		operation = sessionpresentation.EditExternally
	case "\x16":
		operation = sessionpresentation.AttachClipboard
	case "\x03":
		operation = sessionpresentation.CancelComposer
	case "\r":
		operation = sessionpresentation.SubmitComposer
	case "\t":
		operation = sessionpresentation.QueueComposer
		if view.Draft.Available && view.Draft.Prediction != "" && view.Questions.Call == nil && !view.Approvals.Open {
			operation = sessionpresentation.AcceptPrediction
		}
	}
	if operation != sessionpresentation.NoComposerOperation {
		intent := sessionpresentation.ComposerIntent{Thread: view.Input.ID, Version: view.Version, Target: target, Operation: operation}
		if operation == sessionpresentation.RestoreWaiting {
			intent.Work = new(view.Interrupt)
		}
		return ComposerInput{Command: intent}
	}
	secret := view.Questions.Call != nil && view.Questions.Call.Questions[view.Questions.Index].Secret
	if secret {
		draft = prompts.Secret.Draft
	}
	action, handled := m.editKey(draft, key)
	if !handled {
		return ComposerInput{}
	}
	if secret {
		changed, err := prompts.Secret.Apply(action)
		return ComposerInput{LocalEdit: action.Kind, Changed: changed, Err: err}
	}
	return ComposerInput{Editor: sessionpresentation.EditorIntent{Version: view.Version, Target: target, Action: action}}
}
