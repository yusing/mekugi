package launch

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func submitBTW(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, preparation *composerPreparation) error {
	text := strings.TrimSpace((*editor).Text)
	displayingSide := attached.presentation.DisplayingSide()
	if preparation != nil {
		displayingSide = preparation.side
	}
	if *side != nil && (*side).Unloaded && displayingSide {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Side conversation closed · return to Main to start a new conversation", true)
		return nil
	}
	if b := *side; text == "" && b != nil && b.OneOff && displayingSide && len(b.Drafts.Queued) > 0 {
		b.Drafts.Unsent = append(b.Drafts.Unsent, b.Drafts.Queued[0])
		b.Drafts.Queued = b.Drafts.Queued[1:]
		return flushBTW(attached, terminal, control, picker, questions, approvals, clock, settings, side, owned, environment, hook, observations)
	}
	oneoff := sideCommand(text) == "/oneoff"
	if text == "/side" || oneoff {
		if control.Thread == "" || restoring || control.Replacement.Pending() {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the conversation to load before opening a side conversation", false)
			return nil
		}
		if oneoff && strings.TrimSpace(strings.TrimPrefix(text, "/oneoff")) == "" {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Use /oneoff TASK", false)
			return nil
		}
		if oneoff && !preparation.admit((*editor).Draft, owned) {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "The prepared input was canceled or is unavailable.", true)
			return nil
		}
		picker.sideTask = nil
		if *side != nil {
			if oneoff {
				draft := preparation.snapshot((*editor).Draft.Clone())
				picker.sideTask = &draft
				picker.sideTaskDelivery = attached.effects.input()
			}
			if !oneoff {
				deleteDraftRange(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, *side, clock, questions, approvals, overlay, *owned, 0, len((*editor).Text))
			}
			picker.sideTarget = (*side).Key
			picker.openMenu("side-replace")
			picker.Choices = []completion.Choice{{ID: 1, Name: "Cancel", Description: "Keep the current side conversation"}, {ID: 2, Name: "Replace", Description: "Close Side and create a fresh snapshot of Main"}}
			return nil
		}
		draft := preparation.take(*editor)
		if oneoff {
			pending := prepareDraft(picker, terminal, attached.cwd, environment, sideTaskDraft(draft))
			pending.Origin = attached.view
			pending.Delivery = attached.effects.input()
			if err := openFreshSide(attached, terminal, control, editor, side, settings, presentationTime(clock), true, pending); err != nil {
				return err
			}
			return flushBTW(attached, terminal, control, picker, questions, approvals, clock, settings, side, owned, environment, hook, observations)
		}
		return openFreshSide(attached, terminal, control, editor, side, settings, presentationTime(clock), false, conversationmodel.Input{})
	}

	question := text
	command := sideCommand(text) == "/btw"
	if command {
		question = strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	}
	owner := side
	if command {
		owner = &control.BTW
	}
	if question == "" {
		if command {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		}
		return nil
	}
	if control.Thread == "" || restoring || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if (*owner) != nil && ((*owner).Busy || (*owner).Starting) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if (*owner) != nil && (*owner).Status == conversationmodel.SideCompactionRequired {
		setNotice(&attached.feedback, terminal, presentationTime(clock), conversationmodel.SideCompactionRequired, true)
		return nil
	}
	if (*owner) != nil && (*owner).Unloaded {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Side conversation closed · Esc closes", true)
		return nil
	}
	if !preparation.admit((*editor).Draft, owned) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "The prepared input was canceled or is unavailable.", true)
		return nil
	}
	if (*owner) == nil {
		answer := new(conversationmodel.SideAnswer)
		if command {
			attached.conversation.BTW = answer
		} else {
			attached.conversation.Side = answer
		}
		(*owner) = &sessioncontrol.Side{SideAnswer: answer}
	}
	b := (*owner)
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := 0
	if command {
		prefix = strings.Index((*editor).Text, "/btw") + len("/btw")
	}
	if prefix > 0 {
		deleteDraftRange(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, (*owned), 0, prefix)
	}
	pending := prepareDraft(picker, terminal, attached.cwd, environment, preparation.take(*editor))
	pending.Origin = attached.view
	pending.Delivery = attached.effects.input()
	b.Begin(question, pending)
	b.Busy = true
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	return flushBTW(attached, terminal, control, picker, questions, approvals, clock, settings, side, owned, environment, hook, observations)
}

func flushBTW(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, side **sessioncontrol.Side, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations) error {
	if err := flushSide(attached, terminal, control, picker, questions, approvals, clock, settings, side, owned, environment, hook, observations); err != nil {
		return err
	}
	return flushSide(attached, terminal, control, picker, questions, approvals, clock, settings, &control.BTW, owned, environment, hook, observations)
}

func flushSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, side **sessioncontrol.Side, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations) error {
	b := (*side)
	if b != nil && b.OneOff && !b.Closed && !b.Unloaded && !b.Starting {
		for len(b.Drafts.Unsent) > 0 {
			input := b.Drafts.Unsent[0]
			thread, turn := b.Thread, b.Turn
			if input.Question != nil && input.Question.Thread != b.Thread {
				thread = input.Question.Thread
				if agent := attached.session.Activity().Agent(thread); agent != nil {
					turn = agent.Turn
				} else {
					turn = ""
				}
			}
			if control.Requests.SideReplyStarting(thread) || observations != nil && observations.EmittingToolInput(attached.cwd, thread) {
				return nil
			}
			if turn == "" {
				if ready, err := hook.checkThread(control.Requests, thread, attached.cwd, func() { b.Status = "Checking approval guard…" }); !ready || err != nil {
					return err
				}
			}
			if err := control.Requests.ReplySide(b, thread, turn, input); err != nil {
				return err
			}
			b.Drafts.Unsent = b.Drafts.Unsent[1:]
			for _, image := range input.Images {
				delete(*owned, image.Path)
			}
			if turn == "" {
				if thread == b.Thread {
					b.Status, b.Started = "Working", presentationTime(clock)
				}
				hook.readyThread = ""
				return nil
			}
		}
	}
	if !b.Ready(control.Replacement.Pending()) {
		return nil
	}
	if b.OneOff && b.Thread != "" && b.Pending.Text != "" {
		if ready, err := hook.checkThread(control.Requests, b.Thread, attached.cwd, func() { b.Status = "Checking approval guard…" }); !ready || err != nil {
			return err
		}
	}
	parts := []conversationmodel.Input{b.Pending}
	if picker.waitForSkillBindings(attached, terminal, clock, control.Requests, parts) {
		return nil
	}
	picker.bindSkills(attached.cwd, &parts[0], true)
	snapshotDraftSkills(attached.cwd, environment, &parts[0])
	b.Pending = parts[0]
	update, err := control.Requests.StartSide(b, sessioncontrol.SideMain{
		Thread: control.Thread, Model: settings.Model, Effort: settings.Effort, Tier: settings.Tier,
		Blocked: control.Starting() || control.InputPending() || control.Settings.Pending(), Replacing: control.Replacement.Pending(),
	}, presentationTime(clock))
	if update.TurnRequested {
		if b.OneOff {
			hook.readyThread = ""
		}
		for _, image := range b.Pending.Images {
			delete((*owned), image.Path)
		}
	}
	applySideUpdate(attached, terminal, control, clock, update)
	return err
}

func applySideUpdate(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], clock func() time.Time, update sessioncontrol.SideUpdate) {
	if update.ClosedThread != "" {
		forgetSideActivity(attached, update.ClosedThread)
	}
	if update.ReplyThread != "" && attached.session.Transcript(update.ReplyThread, update.ReplyTurn).Turn != nil {
		attached.session.Caller(update.ReplyThread)
		attached.session.Activity().Agent(update.ReplyThread).Turn = update.ReplyTurn
	}
	if update.Restore.Text != "" && update.Side == control.BTW {
		input := conversationmodel.Join(conversationmodel.Input{Text: "/btw"}, update.Restore)
		input.Origin = update.Restore.Origin
		input.Delivery = update.Restore.Delivery
		restored := attached.conversation.RestoreInput(input)
		if attached.restoreInput != nil {
			attached.restoreInput(attached.executionPresentation, restored.Detached)
		}
		setNotice(&attached.feedback, terminal, presentationTime(clock), "BTW input restored to Main", true)
	} else if update.Restore.Text != "" && update.Side != nil {
		if restoreSideDraft(attached, update.Side, update.Restore) {
			update.Notice += " · Enter in an empty One-off composer retries the attachment delivery"
		} else {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Side input restored for retry", true)
		}
	}
	if update.Notice != "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), update.Notice, true)
	}
}

func restoreSideDraft(attached *attachedPresentation, side *sessioncontrol.Side, input conversationmodel.Input) bool {
	queued, restored := side.RestoreInput(input)
	if attached.restoreSide != nil {
		for _, input := range restored.Detached {
			attached.restoreSide(attached.executionPresentation, input, side.Thread)
		}
	}
	return queued
}

func closeBTWQuestion(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest]) error {
	question := control.BTW
	if question == nil {
		return nil
	}
	control.BTW, attached.conversation.BTW = nil, nil
	question.Close()
	return control.Requests.ReleaseSide(question)
}

func closeSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, side **sessioncontrol.Side, owned *map[string]bool) error {
	b := (*side)
	if b == nil {
		return nil
	}
	(*side) = nil
	b.Close()
	if b.Thread != "" {
		threads := control.Requests.SideThreads(b)
		control.RetireThreads(b.Thread, threads...)
		for _, thread := range threads {
			forgetSideActivity(attached, thread)
		}
	}
	for _, c := range questions.calls() {
		if control.Requests.Side(c.Thread) != b {
			continue
		}
		if questions.activeCall() == c {
			*editor = hideQuestionEditor(questions, attached.conversation, terminal, *editor, control.Thread)
		}
		c.Resolve("Conversation closed")
		discardQuestionEditors(terminal.Prompts, c)
		questions.Remove(c.Call)
		delete(questions.handles, c.Call)
	}
	for _, a := range approvals.calls() {
		if control.Requests.Side(a.Thread) != b {
			continue
		}
		hideApprovals(approvals, terminal, &attached.conversation.PromptEditor, *editor)
		approvals.Remove(a.Approval)
		delete(approvals.handles, a.Approval)
	}

	attached.conversation.Side = nil
	attached.presentation.SetSide(sessionpresentation.Thread{})
	if attached.view == "" {
		*editor = &attached.conversation.Editor
	}
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side), attached.nativeImages)
	return control.Requests.ReleaseSide(b)
}

func openFreshSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, side **sessioncontrol.Side, settings preferences.Applied, now time.Time, oneoff bool, pending conversationmodel.Input) error {
	answer := &conversationmodel.SideAnswer{OneOff: oneoff}
	answer.Begin(strings.TrimSpace(pending.Text), pending)
	attached.conversation.Side = answer
	*side = &sessioncontrol.Side{SideAnswer: answer, ForkPending: true, Busy: true}
	target := sessionpresentation.Thread{ID: "side:" + answer.Key, Caller: answer.Caller()}
	attached.presentation.SetSide(target)
	if err := attached.presentation.Display(sessionpresentation.DisplayIntent{Version: attached.presentation.View().Version, Thread: target}); err != nil {
		return err
	}
	if attached.view == "" {
		*editor = &answer.Drafts.Editor
	}
	terminal.Transcript.Follow()
	_, err := control.Requests.StartSide(*side, sessioncontrol.SideMain{Thread: control.Thread, Model: settings.Model, Effort: settings.Effort, Tier: settings.Tier, Blocked: control.Starting() || control.InputPending() || control.Settings.Pending()}, now)
	return err
}

// sideCommand admits exact command boundaries shared by Main and the side composer.
func sideCommand(text string) string {
	if text == "/side" {
		return text
	}
	for _, command := range []string{"/btw", "/oneoff"} {
		if text == command || strings.HasPrefix(text, command+" ") || strings.HasPrefix(text, command+"\n") || strings.HasPrefix(text, command+"\t") {
			return command
		}
	}
	return ""
}

func sideTaskDraft(draft composer.Draft) composer.Draft {
	local := composer.Editor{Draft: draft}
	local.Apply(composer.Action{Kind: composer.DeleteAction, Start: 0, End: strings.Index(local.Text, "/oneoff") + len("/oneoff")})
	result := local.Draft.Clone()
	result.CursorBack = 0
	return result
}

// forgetSideActivity retires presentation only, not processes or retained evidence.
func forgetSideActivity(attached *attachedPresentation, thread string) {
	attached.session.Activity().ForgetAgent(thread)
	for key := range attached.session.Commands() {
		if key[0] == thread {
			delete(attached.session.Commands(), key)
		}
	}
	for i, v := range slices.Backward(attached.childSource.Entries) {
		entry := v.Source
		if entry.Native != nil && entry.Native.Thread == thread {
			attached.childSource.Model.Remove(i, i+1)
		}
	}
}
