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
				if err := u.applyInput(u.shell, u.shell.FrameInput(presentationTime(u.clock), mekugiVersion()).Dispatch(&u.shell.Interaction, event)); err != nil || u.quitRequested {
					return err
				}
			}
		default:
			if u.shell.Decoder.Escape(time.Now()) {
				return u.applyInput(u.shell, u.shell.FrameInput(presentationTime(u.clock), mekugiVersion()).Send(&u.shell.Interaction, "\x1b"))
			}
			return nil
		}
	}
	return nil
}

// prepareComposerInput admits terminal-selected prompt suspension before editing.
func (u *appServerUI) prepareComposerInput() bool {
	ready := u.shell.Prompts.Ready()
	u.publishPresentation(presentationTime(u.clock))
	preparation := u.shell.Main.PrepareInput(ready)
	if !preparation.Editable {
		return false
	}
	if intent := preparation.Question; intent != nil {
		if err := u.attached.presentation.QuestionInput(*intent, u.questions.Questions); err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
			return false
		}
		u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	}
	if intent := preparation.Approval; intent != nil {
		if err := u.attached.presentation.ApprovalInput(*intent, u.approvals.Approvals); err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
			return false
		}
		hideApprovals(&u.approvals, u.shell, &u.attached.conversation.PromptEditor, u.Editor)
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
			u.shell.Main.CancelPaste()
			if !u.prepareComposerInput() {
				return false, nil
			}
			u.Run = composer.NoRun
		}
		if event.Kind != terminalsession.PasteInput {
			u.publishPresentation(presentationTime(u.clock))
		}
		pasted, err := u.shell.Main.PasteInput(event, u.shell.Prompts)
		if err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		} else if pasted != nil {
			u.applyPaste(*pasted)
		}
	}
	return false, nil
}
