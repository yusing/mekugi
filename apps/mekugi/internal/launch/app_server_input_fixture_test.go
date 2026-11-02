package launch

import (
	"context"
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// drainKeys consumes queued continuation bytes before deciding that a pending
// Escape was a standalone key. Rendering and tick work can exceed the Escape
// timeout even when the terminal delivered a complete sequence promptly.
func (session *nativeSession) drainKeys(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, quit *bool, keys <-chan byte) error {
	// Bound work so continuous mouse motion or a large paste cannot starve
	// rendering and host events. Exhausting the budget defers Escape expiry.
	for range 256 {
		select {
		case key, ok := <-keys:
			if !ok {
				return io.EOF
			}
			(*dirty) = true
			if event := terminal.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
				if err := session.applyInput(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, quit, terminal, terminal.FrameInput(presentationTime(clock), mekugiVersion()).Dispatch(&terminal.Interaction, event)); err != nil || (*quit) {
					return err
				}
			}
		default:
			if terminal.Decoder.Escape(time.Now()) {
				return session.applyInput(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, quit, terminal, terminal.FrameInput(presentationTime(clock), mekugiVersion()).Send(&terminal.Interaction, "\x1b"))
			}
			return nil
		}
	}
	return nil
}

// prepareComposerInput admits terminal-selected prompt suspension before editing.
func prepareComposerInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock) bool {
	ready := terminal.Prompts.Ready()
	publishPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side)
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
func (session *nativeSession) input(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, dirty *bool, files *detailFiles, event terminalsession.Input) (bool, error) {
	if event.Kind == terminalsession.NoInput {
		return false, nil
	}
	defer func() {
		settlePresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, session.overlayOpen(*panel, *resumePicker))
	}()
	switch event.Kind {
	case terminalsession.KeyInput:
		return session.applyMainKey(attached, terminal, picker, editor, clock, side, panel, resumePicker, parent, dirty, files, mainKeyEvent(event.Text))
	case terminalsession.PasteStart, terminalsession.PasteInput, terminalsession.PasteEnd:
		if event.Kind == terminalsession.PasteStart {
			terminal.Main.CancelPaste()
			if !prepareComposerInput(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals) {
				return false, nil
			}
			(*editor).Run = composer.NoRun
		}
		if event.Kind != terminalsession.PasteInput {
			publishPresentation(attached, terminal, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side))
		}
		pasted, err := terminal.Main.PasteInput(event, terminal.Prompts)
		if err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		} else if pasted != nil {
			applyPaste(attached, terminal, &session.Client, picker, editor, &session.questions, &session.approvals, clock, side, &session.ownedImages, session.skillEnvironment, parent, *pasted)
		}
	}
	return false, nil
}
