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

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

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
	shellEdits.pending.Discard()
	shellEdits.pending = nil
	edit := captureShellEdit(attached, terminal, control, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command)
	if err := control.Request("thread/shellCommand", map[string]any{"threadId": control.Thread, "command": command}); err != nil {
		edit.Discard()
		return err // Keep the draft when transport submission fails.
	}
	shellEdits.pending = edit
	attached.conversation.Shell = prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
	control.Shell.Begin(control.Turn)
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell command submitted", false)
	terminal.Transcript.Follow()
	return nil
}

func shellResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, clock func() time.Time, shellEdits *appServerShellEdits, failure *appserver.Error) {
	draft := attached.conversation.Shell
	attached.conversation.Shell = conversationmodel.Input{}
	control.Shell.Acknowledge(failure != nil, control.Turn)
	if failure != nil {
		shellEdits.pending.Discard()
		shellEdits.pending = nil
		hint := ""
		if (*editor).Text == "" {
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
