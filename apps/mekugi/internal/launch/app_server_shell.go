// Source: apps/mekugi/internal/launch/app_server_shell.go:1:167@[14a6d1f3]
package launch

import (
	"context"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Auxiliary turns execute and retain output in Codex without
// acquiring or completing logical model work.
func observeLogicalHost(driver *threadbinding.Driver, live *sessioncontrol.Live, message appserver.Message) error {
	if message.Method == "turn/started" || message.Method == "turn/completed" {
		p, err := binding.DecodeNotification(message.Method, message.Params)
		if err != nil {
			return err
		}
		if message.Method == "turn/completed" && p.ThreadID == live.Thread && live.CompactionStopsWait(p.Turn.ID) {
			driver.CheckpointWait(p.ThreadID, p.Turn.ID)
			return nil
		}
		if p.ThreadID == live.Thread && live.AuxiliaryTurn(p.Turn.ID) {
			return nil
		}
	}
	return driver.Observe(message.Method, message.Params)
}

// Source: codex-rs/tui/src/chatwidget/input_submission.rs:36:67@1cc7e236.
// Codex owns execution, cancellation and history: active shell results inject
// into the running turn; standalone results wait in history for the next input.
func submitShell(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, clock func() time.Time, restoring bool, environment []string, parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store)) error {
	command := strings.TrimSpace(strings.TrimPrefix((*editor).Text, "!"))
	if command == "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Type a shell command after !", false)
		return nil
	}
	if control.Thread == "" || restoring {
		return nil
	}
	if !control.AcceptsShell() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Waiting for the pending request · press Enter again when ready", false)
		return nil
	}
	if len((*editor).Images) != 0 || len((*editor).Skills) != 0 || len((*editor).Files) != 0 || len((*editor).Selections) != 0 {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell commands use plain text · remove attachments and picker tokens first", true)
		return nil
	}
	delivery := attached.effects.input()
	if err := dispatchShell(attached, terminal, control, clock, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command); err != nil {
		return err
	}
	attached.conversation.Shell = prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
	attached.conversation.Shell.Origin = attached.view
	attached.conversation.Shell.Delivery = delivery
	attached.conversation.ShellFromEditor = true
	return nil
}

func dispatchShell(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], clock func() time.Time, parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), command string) error {
	shellEdits.pending.Discard()
	shellEdits.pending = nil
	edit := captureShellEdit(attached, terminal, control, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command)
	if err := control.Request("thread/shellCommand", map[string]any{"threadId": control.Thread, "command": command}); err != nil {
		edit.Discard()
		return err // Keep the draft when transport submission fails.
	}
	shellEdits.pending = edit
	control.Shell.Begin(control.Turn)
	if control.LocalStop != "" {
		control.Shell.WakeAfterSettlement()
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell command submitted", false)
	terminal.Transcript.Follow()
	return nil
}

func shellResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, clock func() time.Time, shellEdits *appServerShellEdits, failure *appserver.Error) {
	if r := control.Rewind; r != nil && r.Sent {
		control.Shell.Acknowledge(failure != nil, control.Turn)
		if failure != nil {
			shellEdits.pending.Discard()
			shellEdits.pending = nil
			control.Rewind = nil
			control.Replacement.Finish()
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Revert rejected: "+failure.Message, true)
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
		if draft.Origin != "" && attached.restoreShell != nil {
			attached.restoreShell(attached.executionPresentation, draft)
			rememberInput(attached.conversation, draft)
		} else if fromEditor && (*editor).Text == "" {
			(*editor).Replace(draft.Draft)
		} else {
			// Never join a later prose draft onto an executable shell script.
			rememberInput(attached.conversation, draft)
			hint = " · command saved in input history (↑)"
		}
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell command rejected: "+failure.Message+hint, true)
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
func submitAPIShell(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, restoring bool, environment []string, parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "Type a shell command.", nil
	}
	if reason := apiInputRefusal(attached, control, restoring); reason != "" {
		return reason, nil
	}
	if !control.AcceptsShell() {
		return "Wait for the pending request before submitting a shell command.", nil
	}
	if err := dispatchShell(attached, terminal, control, clock, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command); err != nil {
		return "", err
	}
	attached.conversation.Shell = prepareDraft(picker, terminal, attached.cwd, environment, composer.Draft{Text: "!" + command})
	attached.conversation.ShellFromEditor = false
	return "", nil
}
