package router

import (
	"strings"

	"github.com/yusing/mekugi/internal/appserver"
)

func (u *appServerUI) shellMode() bool {
	return u.currentQuestion() == nil && strings.HasPrefix(u.draft, "!")
}

// Source: codex-rs/tui/src/chatwidget/input_submission.rs:36:67@1cc7e236.
// Codex owns execution, cancellation and history: active shell results inject
// into the running turn; standalone results wait in history for the next input.
func (u *appServerUI) submitShell() error {
	command := strings.TrimSpace(strings.TrimPrefix(u.draft, "!"))
	if command == "" {
		u.setNotice("Type a shell command after !", false)
		return nil
	}
	if u.thread == "" || u.restoring != nil {
		return nil
	}
	if u.clearing || u.shellPending.text != "" || u.shellOrigin != nil || u.starting || u.submission.text != "" || u.interrupting != "" {
		u.setNotice("Waiting for the pending request · press Enter again when ready", false)
		return nil
	}
	if len(u.images) != 0 || len(u.skills) != 0 || len(u.files) != 0 {
		u.setNotice("Shell commands use plain text · remove attachments and picker tokens first", true)
		return nil
	}
	if err := u.request("thread/shellCommand", map[string]any{"threadId": u.thread, "command": command}); err != nil {
		return err // Keep the draft when transport submission fails.
	}
	u.shellPending = u.takeDraft()
	u.shellOrigin = new(u.turn)
	if u.turn == "" {
		u.shellStandalone, u.starting = true, true
	}
	u.setNotice("Shell command submitted", false)
	u.view.follow()
	return nil
}

func (u *appServerUI) shellResponse(failure *appserver.Error) {
	draft := u.shellPending
	u.shellPending = composerDraft{}
	if failure != nil {
		u.shellOrigin = nil
		if u.shellStandalone && u.turn == "" {
			u.shellStandalone, u.starting = false, false
		}
		hint := ""
		if u.draft == "" {
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

func (u *appServerUI) observeShellItem(thread string, item appServerItem) {
	// Called only for starts: an older auxiliary command can complete while a
	// newer shell request is still awaiting its execution identity.
	if thread == u.thread && item.Type == "commandExecution" && item.Source == "userShell" {
		u.shellOrigin = nil
	}
}
