package router

import (
	"io"
	"time"

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
			if event := u.shell.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
				if err := u.shell.applyInput(u.shell.frameInput().Dispatch(&u.shell.Interaction, event)); err != nil || u.quitRequested {
					return err
				}
			}
		default:
			if u.shell.Decoder.Escape(time.Now()) {
				return u.shell.applyInput(u.shell.frameInput().Send(&u.shell.Interaction, "\x1b"))
			}
			return nil
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
		return u.applyMainKey(terminalsession.KeyEvent(event.Text))
	case terminalsession.PasteStart, terminalsession.PasteInput, terminalsession.PasteEnd:
		if event.Kind == terminalsession.PasteStart {
			u.mainView.CancelPaste()
			if !u.prepareComposerInput() {
				return false, nil
			}
			u.Run = composer.NoRun
		}
		if event.Kind != terminalsession.PasteInput {
			u.publishPresentation(u.now())
		}
		pasted, err := u.mainView.PasteInput(event, &u.prompts)
		if err != nil {
			u.setNotice(err.Error(), true)
		} else if pasted != nil {
			u.applyPaste(*pasted)
		}
	}
	return false, nil
}
