package router

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func editInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, intent sessionpresentation.DraftEditIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	if err := attached.presentation.EditDraft(intent, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages(owned, (*editor), attached.conversation, questions, approvals, terminal.Prompts, side)
	return true
}

func (u *appServerUI) submitInput(intent sessionpresentation.SubmitIntent) error {
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	draft, plan, err := u.attached.presentation.TakeInput(intent, &u.Live, u.attached.conversation, u.restoring != nil)
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return nil
	}
	input := prepareDraft(&u.picker, u.shell, u.attached.cwd, u.skillEnvironment, draft)
	u.attached.conversation.QueueInput(input, plan.Queued)
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	if !plan.Flush {
		return nil
	}
	u.shell.Transcript.Follow()
	return u.flushInput()
}

// applyComposerInput resolves terminal output through the current owners. Every
// semantic action retains the publication and editor target it came from.
func (u *appServerUI) applyComposerInput(input terminalsession.ComposerInput) (bool, error) {
	if input.Err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), input.Err.Error(), true)
		return false, nil
	}
	if input.Command.Operation != sessionpresentation.NoComposerOperation {
		return u.applyComposerCommand(input.Command)
	}
	kind, changed := input.LocalEdit, input.Changed
	if input.Editor.Action.Kind != composer.NoAction {
		var err error
		changed, err = u.attached.presentation.ApplyEditor(input.Editor, u.attached.conversation)
		if err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
			return false, nil
		}
		kind = input.Editor.Action.Kind
	}
	switch kind {
	case composer.NoAction, composer.MoveAction:
		return false, nil
	case composer.RecallPreviousAction, composer.RecallNextAction:
		if !changed {
			return false, nil
		}
		u.shell.Main.CursorColumn = nil
	case composer.DeleteAction:
		if !changed {
			return false, nil
		}
	}
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	if kind != composer.UndoAction && kind != composer.RedoAction {
		pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
	}
	return false, nil
}

func (u *appServerUI) applyComposerCommand(intent sessionpresentation.ComposerIntent) (bool, error) {
	if err := u.attached.presentation.AdmitComposer(intent, u.attached.conversation); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return false, nil
	}
	switch intent.Operation {
	case sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort:
		return false, u.stepReasoning(intent.Operation == sessionpresentation.IncreaseEffort)
	case sessionpresentation.RestoreWaiting:
		u.editQueued()
	case sessionpresentation.EditExternally:
		if q := u.questions.Current(); q != nil && q.IsSecret {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "External editor is unavailable for secret answers", false)
			return false, nil
		}
		return false, &composerEditorRequest{intent: intent}
	case sessionpresentation.AttachClipboard:
		u.pasteImage(intent)
	case sessionpresentation.CancelComposer:
		if u.Replacement.Archiving() {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Archiving session…", false)
			return false, nil
		}
		// As in Codex, Ctrl-C first clears the draft (undoable with Ctrl+Z),
		// then cancels queued compaction, interrupts the active turn, or quits.
		if u.Text != "" {
			deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again quits", false)
			if u.Turn != "" || u.Starting() || u.InputPending() {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Ctrl-C again interrupts", false)
			}
			if u.shell.Main.InterruptLocked {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Draft cleared · Ctrl+Z restores · Locked · /unlock", false)
			}
			return false, nil
		}
		if u.cancelQueuedCompact() {
			return false, nil
		}
		if u.Turn != "" || u.Starting() || u.InputPending() || u.Compaction.Pending() || u.Compaction.ContinuationPending() || len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) > 0 {
			return false, u.keyboardInterrupt()
		}
		if u.shell.Main.InterruptLocked {
			u.lockNotice()
			return false, nil
		}
		return true, nil
	case sessionpresentation.SubmitComposer:
		if u.Replacement.Archiving() {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Archiving session…", false)
			return false, nil
		}
		text := strings.TrimSpace(u.Text)
		if text == "/archive" {
			u.showArchivePicker()
			return false, nil
		}
		if handled, err := u.controlsCommand(text); handled {
			return false, err
		}
		if u.titleCommand(text) {
			return false, nil
		}
		if text == "/btw" || strings.HasPrefix(text, "/btw ") || strings.HasPrefix(text, "/btw\n") || strings.HasPrefix(text, "/btw\t") {
			return false, u.submitBTW()
		}
		if u.attached.conversation.ShellMode() {
			return false, u.submitShell()
		}
		if text == "/compact" || text == "/clear" {
			return false, u.sessionCommand(text)
		}
		if text == "/resume" || strings.HasPrefix(text, "/resume ") {
			return false, u.resumeCommand(text)
		}
		if text == "/ps" {
			u.showRunningProcesses()
			return false, nil
		}
		if text == "/session" {
			u.showSessionMetrics()
			return false, nil
		}
		if text == "/status" {
			return false, u.showStatus()
		}
		if text == "/copy" {
			u.showCopyPicker()
			return false, nil
		}
		if text == "/skills" {
			deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
			u.picker.openMenu("menu")
			u.picker.Choices = []completion.Choice{{Name: "list"}, {Name: "manage"}}
			return false, nil
		}
		if text == "/quit" {
			if u.Turn == "" && !u.Starting() && !u.InputPending() {
				u.Text = ""
				u.Editor.Changed()
				return true, nil
			}
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Interrupt the active turn before quitting", false)
			return false, nil
		}
		if handled, err := u.settingsCommand(text); handled {
			return false, err
		}
		if strings.HasPrefix(text, "/") {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Unknown command "+strings.Fields(text)[0]+" · /title, /compact, /clear, /resume, /archive, /btw, /status, /session, /ps, /copy, /skills, /model, /effort, /reasoning, /tier, /live, /quit", true)
			return false, nil
		}
		if text == "" || u.Thread == "" || u.restoring != nil {
			return false, nil
		}
		view := u.attached.presentation.View()
		return false, u.submitInput(sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.SendInput})
	case sessionpresentation.QueueComposer:
		// Tab queues busy input for the next turn; otherwise it sends like Enter.
		text := strings.TrimSpace(u.Text)
		if text == "/compact" {
			return false, u.submitCompact(true)
		}
		if u.attached.conversation.ShellMode() || text == "" || strings.HasPrefix(text, "/") || u.Turn == "" && !u.Starting() && !u.InputPending() {
			intent.Operation = sessionpresentation.SubmitComposer
			return u.applyComposerCommand(intent)
		}
		view := u.attached.presentation.View()
		return false, u.submitInput(sessionpresentation.SubmitIntent{Version: view.Version, Revision: view.Draft.Revision, Mode: sessioncontrol.QueueNextTurn})
	}
	return false, nil
}

func (u *appServerUI) applyPromptInput(input terminalsession.PromptInput) (bool, error) {
	if input.Approval.Request != "" {
		return u.applyApprovalInput(input)
	}
	return u.applyQuestionInput(input)
}
