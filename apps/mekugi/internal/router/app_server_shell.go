package router

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Source: codex-rs/tui/src/chatwidget/input_submission.rs:36:67@1cc7e236.
// Codex owns execution, cancellation and history: active shell results inject
// into the running turn; standalone results wait in history for the next input.
func (u *appServerUI) submitShell() error {
	command := strings.TrimSpace(strings.TrimPrefix(u.Text, "!"))
	if command == "" {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Type a shell command after !", false)
		return nil
	}
	if u.Thread == "" || u.restoring != nil {
		return nil
	}
	if !u.AcceptsShell() {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Waiting for the pending request · press Enter again when ready", false)
		return nil
	}
	if len(u.Images) != 0 || len(u.Skills) != 0 || len(u.Files) != 0 || len(u.Selections) != 0 {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Shell commands use plain text · remove attachments and picker tokens first", true)
		return nil
	}
	u.shellEdits.pending.Discard()
	u.shellEdits.pending = nil
	edit := u.captureShellEdit(command)
	if err := u.Request("thread/shellCommand", map[string]any{"threadId": u.Thread, "command": command}); err != nil {
		edit.Discard()
		return err // Keep the draft when transport submission fails.
	}
	u.shellEdits.pending = edit
	u.attached.conversation.Shell = prepareDraft(&u.picker, u.shell, u.attached.cwd, u.skillEnvironment, u.Editor.Take())
	u.Shell.Begin(u.Turn)
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Shell command submitted", false)
	u.shell.Transcript.Follow()
	return nil
}

func (u *appServerUI) shellResponse(failure *appserver.Error) {
	draft := u.attached.conversation.Shell
	u.attached.conversation.Shell = conversationmodel.Input{}
	u.Shell.Acknowledge(failure != nil, u.Turn)
	if failure != nil {
		u.shellEdits.pending.Discard()
		u.shellEdits.pending = nil
		hint := ""
		if u.Text == "" {
			u.Editor.Replace(draft.Draft)
		} else {
			// Never join a later prose draft onto an executable shell script.
			rememberInput(u.attached.conversation, draft)
			hint = " · command saved in input history (↑)"
		}
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Shell command rejected: "+failure.Message+hint, true)
		return
	}
	rememberInput(u.attached.conversation, draft)
}

func observeShellItem(shell *sessioncontrol.Shell, main string, thread string, item binding.ThreadItem) {
	// Called only for starts: an older auxiliary command can complete while a
	// newer shell request is still awaiting its execution identity.
	if thread == main && item.Type == "commandExecution" && item.Source == "userShell" {
		shell.Identified()
	}
}
