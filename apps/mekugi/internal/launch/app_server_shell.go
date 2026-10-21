package launch

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/completion"
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
	if err := dispatchShell(attached, terminal, control, clock, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command); err != nil {
		return err
	}
	attached.conversation.Shell = prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
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

func dispatchRewind(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store)) {
	r := control.Rewind
	if r == nil || r.Sent {
		return
	}
	selection := picker.rewind
	if selection == nil || reviewOwner == nil || reviewOwner.Failure != "" || reviewOwner.Restoring() || reviewOwner.Projection.Data != selection.snapshot || !maps.EqualFunc(reviewOwner.Scope().Workspaces, selection.scope.Workspaces, maps.Equal[map[string]bool, map[string]bool]) {
		control.Rewind = nil
		picker.openMenu("rewind-edits")
		picker.Loading = false
		picker.Choices = []completion.Choice{{ID: 1, Name: "Keep edits"}}
		if selection != nil {
			selection.problem = "saved changes changed; reopen /rewind to refresh"
		}
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Saved changes changed; reopen /rewind to refresh before reverting", true)
		return
	}
	command := r.Command
	if err := dispatchShell(attached, terminal, control, clock, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command); err != nil {
		control.Rewind = nil
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not revert edits: "+err.Error(), true)
		return
	}
	r.Sent = true
	control.Replacement.Rewind()
	attached.presentation.Status.SetText("Reverting captured edits…")
}
