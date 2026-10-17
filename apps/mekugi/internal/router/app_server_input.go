package router

import (
	"io"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// drainKeys consumes queued continuation bytes before deciding that a pending
// Escape was a standalone key. Rendering and tick work can exceed the Escape
// timeout even when the terminal delivered a complete sequence promptly.
func (u *appServerUI) drainKeys(keys <-chan byte) error {
	// Bound work so continuous mouse motion or a large paste cannot starve
	// rendering and host events. Exhausting the budget defers Escape expiry.
	for range 256 {
		select {
		case key, ok := <-keys:
			if !ok {
				return io.EOF
			}
			u.dirty = true
			if err := u.shell.key(key); err != nil || u.quitRequested {
				return err
			}
		default:
			return u.shell.flushEscape()
		}
	}
	return nil
}

// prepareComposerInput keeps an unseen prompt from taking input intended for the draft.
func (u *appServerUI) prepareComposerInput() bool {
	if u.composerReadOnly() {
		return false
	}
	if u.activeQuestionCall() != nil && !u.prompts.Ready() {
		u.hideQuestions()
	}
	if u.approvals.Open && !u.prompts.Ready() {
		u.hideApprovals()
	}
	return true
}

// input consumes complete keys and framed paste events, never terminal byte sequences.
func (u *appServerUI) input(event terminalsession.Input) (bool, error) {
	if event.Kind == terminalsession.NoInput {
		return false, nil
	}
	defer u.settlePresentation()
	switch event.Kind {
	case terminalsession.KeyInput:
		return u.key(event.Text)
	case terminalsession.PasteStart:
		u.pasted = nil
		if u.prepareComposerInput() {
			u.keybindings, u.Run = false, composer.NoRun
			u.pasted = make([]byte, 0)
		}
	case terminalsession.PasteInput:
		if u.pasted != nil {
			u.pasted = append(u.pasted, event.Text...)
		}
	case terminalsession.PasteEnd:
		payload := u.pasted
		u.pasted = nil
		if payload != nil && !u.composerReadOnly() {
			u.paste(payload)
		}
	}
	return false, nil
}
