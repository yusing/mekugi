// Source: apps/mekugi/internal/launch/app_server_shell.go:1:167@[14a6d1f3]
package launch

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Auxiliary turns execute and retain output in Codex without
// acquiring or completing logical model work.
func observeLogicalHost(driver *threadbinding.Driver, live *sessioncontrol.Live, message appserver.Message) (<-chan error, error) {
	if message.Method == "turn/started" || message.Method == "turn/completed" {
		p, err := binding.DecodeNotification(message.Method, message.Params)
		if err != nil {
			return nil, err
		}
		if message.Method == "turn/completed" && p.ThreadID == live.Thread && live.CompactionStopsWait(p.Turn.ID) {
			driver.CheckpointWait(p.ThreadID, p.Turn.ID)
			return nil, nil
		}
		if p.ThreadID == live.Thread && live.AuxiliaryTurn(p.Turn.ID) {
			return nil, nil
		}
	}
	return driver.Observe(message.Method, message.Params)
}

// Source: codex-rs/tui/src/chatwidget/input_submission.rs:36:67@1cc7e236.
// Codex owns execution, cancellation and history: active shell results inject
// into the running turn; standalone results wait in history for the next input.
func (session *nativeSession) submitShell(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, parent context.Context) error {
	command := strings.TrimSpace(strings.TrimPrefix((*editor).Text, "!"))
	if command == "" {
		setNotice(&attached.feedback, presentationTime(clock), "Type a shell command after !", false)
		return nil
	}
	if session.Client.Thread == "" || session.restoring != nil {
		return nil
	}
	if !session.Client.AcceptsShell() {
		setNotice(&attached.feedback, presentationTime(clock), "Waiting for the pending request · press Enter again when ready", false)
		return nil
	}
	if len((*editor).Images) != 0 || len((*editor).Skills) != 0 || len((*editor).Files) != 0 || len((*editor).Selections) != 0 {
		setNotice(&attached.feedback, presentationTime(clock), "Shell commands use plain text · remove attachments and picker tokens first", true)
		return nil
	}
	delivery := attached.effects.input()
	if err := session.dispatchShell(attached, clock, parent, command); err != nil {
		return err
	}
	attached.conversation.Shell = prepareDraft(picker, attached.cwd, session.skillEnvironment, (*editor).Take())
	attached.conversation.Shell.Origin = attached.view
	attached.conversation.Shell.Delivery = delivery
	attached.conversation.ShellFromEditor = true
	return nil
}

func (session *nativeSession) dispatchShell(attached *attachedPresentation, clock func() time.Time, parent context.Context, command string) error {
	session.shellEdits.pending.Discard()
	session.shellEdits.pending = nil
	edit := session.captureShellEdit(attached, parent, command)
	if err := session.Client.Request("thread/shellCommand", map[string]any{"threadId": session.Client.Thread, "command": command}); err != nil {
		edit.Discard()
		return err // Keep the draft when transport submission fails.
	}
	session.shellEdits.pending = edit
	session.Client.Shell.Begin(session.Client.Turn)
	if session.Client.LocalStop != "" {
		session.Client.Shell.WakeAfterSettlement()
	}
	setNotice(&attached.feedback, presentationTime(clock), "Shell command submitted", false)
	return nil
}

func shellResponse(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, clock func() time.Time, shellEdits *appServerShellEdits, failure *appserver.Error) {
	if r := control.Rewind; r != nil && r.Sent {
		control.Shell.Acknowledge(failure != nil, control.Turn)
		if failure != nil {
			shellEdits.pending.Discard()
			shellEdits.pending = nil
			control.Rewind = nil
			control.Replacement.Finish()
			setNotice(&attached.feedback, presentationTime(clock), "Revert rejected: "+failure.Message, true)
		}
		return
	}
	draft := attached.conversation.Shell
	fromEditor := attached.conversation.ShellFromEditor
	attached.conversation.Shell = conversationmodel.Input{}
	attached.conversation.ShellFromEditor = false
	control.Shell.Acknowledge(failure != nil, control.Turn)
	if failure != nil {
		shellEdits.pending.Discard()
		shellEdits.pending = nil
		hint := ""
		if draft.Origin != "" {
			attached.hooks.restoreShell(attached.executionPresentation, draft)
			rememberInput(attached.conversation, draft)
		} else if fromEditor && (*editor).Text == "" {
			(*editor).Replace(draft.Draft)
		} else {
			// Never join a later prose draft onto an executable shell script.
			rememberInput(attached.conversation, draft)
			hint = " · command saved in input history (↑)"
		}
		setNotice(&attached.feedback, presentationTime(clock), "Shell command rejected: "+failure.Message+hint, true)
		return
	}
	rememberInput(attached.conversation, draft)
	if h := attached.inputHistory; h != nil {
		history := attached.conversation.Editor.InputHistory
		if len(history) > 0 {
			history[len(history)-1].Source = h.thread
		}
		h.remember(draft.Text)
	}
}

func observeShellItem(shell *sessioncontrol.Shell, main string, thread string, item binding.ThreadItem) {
	// Called only for starts: an older auxiliary command can complete while a
	// newer shell request is still awaiting its execution identity.
	if thread == main && item.Type == "commandExecution" && item.Source == "userShell" {
		shell.Identified()
	}
}

// submitAPIShell borrows native shell dispatch without consuming the terminal's
// editor. Host acknowledgment and execution use the existing shell lifecycle.
func (session *nativeSession) submitAPIShell(attached *attachedPresentation, picker *composerPicker, clock func() time.Time, parent context.Context, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "Type a shell command.", nil
	}
	if reason := apiInputRefusal(attached, &session.Client, session.restoring != nil); reason != "" {
		return reason, nil
	}
	if !session.Client.AcceptsShell() {
		return "Wait for the pending request before submitting a shell command.", nil
	}
	if err := session.dispatchShell(attached, clock, parent, command); err != nil {
		return "", err
	}
	attached.conversation.Shell = prepareDraft(picker, attached.cwd, session.skillEnvironment, composer.Draft{Text: "!" + command})
	attached.conversation.ShellFromEditor = false
	return "", nil
}
