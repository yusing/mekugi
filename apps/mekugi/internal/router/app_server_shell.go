package router

import (
	"strings"

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
		u.setNotice("Type a shell command after !", false)
		return nil
	}
	if u.Thread == "" || u.restoring != nil {
		return nil
	}
	if !u.AcceptsShell() {
		u.setNotice("Waiting for the pending request · press Enter again when ready", false)
		return nil
	}
	if len(u.Images) != 0 || len(u.Skills) != 0 || len(u.Files) != 0 || len(u.Selections) != 0 {
		u.setNotice("Shell commands use plain text · remove attachments and picker tokens first", true)
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
	u.conversation().Shell = u.takeDraft()
	u.Shell.Begin(u.Turn)
	u.setNotice("Shell command submitted", false)
	u.view.Follow()
	return nil
}

func (u *appServerUI) shellResponse(failure *appserver.Error) {
	draft := u.conversation().Shell
	u.conversation().Shell = conversationmodel.Input{}
	u.Shell.Acknowledge(failure != nil, u.Turn)
	if failure != nil {
		u.shellEdits.pending.Discard()
		u.shellEdits.pending = nil
		hint := ""
		if u.Text == "" {
			u.loadDraft(draft)
		} else {
			// Never join a later prose draft onto an executable shell script.
			u.rememberInput(draft)
			hint = " · command saved in input history (↑)"
		}
		u.setNotice("Shell command rejected: "+failure.Message+hint, true)
		return
	}
	u.rememberInput(draft)
}

func (u *appServerUI) observeShellItem(thread string, item binding.ThreadItem) {
	// Called only for starts: an older auxiliary command can complete while a
	// newer shell request is still awaiting its execution identity.
	if thread == u.Thread && item.Type == "commandExecution" && item.Source == "userShell" {
		u.Shell.Identified()
	}
}
