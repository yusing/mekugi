package router

import (
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

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
func prepareComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock) bool {
	ready := terminal.Prompts.Ready()
	publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, presentationTime(clock))
	preparation := terminal.Main.PrepareInput(ready)
	if !preparation.Editable {
		return false
	}
	if intent := preparation.Question; intent != nil {
		if err := attached.presentation.QuestionInput(*intent, questions.Questions); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false
		}
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	}
	if intent := preparation.Approval; intent != nil {
		if err := attached.presentation.ApprovalInput(*intent, approvals.Approvals); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return false
		}
		hideApprovals(approvals, terminal, &attached.conversation.PromptEditor, (*editor))
	}
	return true
}

// input consumes complete keys and framed paste events, never terminal byte sequences.
func (u *appServerUI) input(event terminalsession.Input) (bool, error) {
	if event.Kind == terminalsession.NoInput {
		return false, nil
	}
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	switch event.Kind {
	case terminalsession.KeyInput:
		return u.applyMainKey(terminalsession.KeyEvent(event.Text))
	case terminalsession.PasteStart, terminalsession.PasteInput, terminalsession.PasteEnd:
		if event.Kind == terminalsession.PasteStart {
			u.shell.Main.CancelPaste()
			if !prepareComposerInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals) {
				return false, nil
			}
			u.Run = composer.NoRun
		}
		if event.Kind != terminalsession.PasteInput {
			publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
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
