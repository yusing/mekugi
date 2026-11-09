package launch

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

// Busy input follows Codex's composer queue: Enter steers the running turn,
// Tab queues input for the next turn. Ctrl-C restores uncommitted input;
// Escape expedites pending steers after interrupting the turn. Unlike Codex,
// waiting input stacks:
// unsent steers, and separately queued messages, go out as one message joined
// by newlines instead of one message per turn.
// Source: codex-rs/tui/src/chatwidget/{input_queue,input_flow,input_restore}.rs
// @86be5320b068ef67b56348b02aa8c33706955da6.

func prepareDraft(picker *composerPicker, workspace string, environment []string, draft composer.Draft) conversationmodel.Input {
	d := conversationmodel.Input{Draft: draft}
	d.ReplyCount = len(binding.QuestionReplies(d.Text))
	picker.bindSkills(workspace, &d, true)
	if len(d.Attachments) == 0 {
		d.SnapshotFiles(workspace)
	}
	snapshotDraftSkills(workspace, environment, &d)
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func restoreDrafts(attached *attachedPresentation, questions *nativeQuestionDock, approvals *nativeApprovalDock, picker *composerPicker, turn string, clock func() time.Time, parts ...conversationmodel.Input) {
	applyInputRestoration(attached, questions, approvals, picker, turn, clock, attached.conversation.RestoreInput(parts...))
}

func applyInputRestoration(attached *attachedPresentation, questions *nativeQuestionDock, approvals *nativeApprovalDock, picker *composerPicker, turn string, clock func() time.Time, restored conversationmodel.InputRestoration) {
	if len(restored.Detached) > 0 {
		attached.hooks.restoreInput(attached.executionPresentation, restored.Detached)
	}
	for _, id := range restored.Rejected {
		for _, call := range questions.calls() {
			if call.ID == id {
				renderQuestionRecord(attached, turn, presentationTime(clock), call)
				break
			}
		}
	}
	if restored.Parked {
		if approvals.Open {
			focusPrompt(picker)
		}
	}
}

// editQueued returns all locally waiting input to the composer as one stack.
// Already-sent steers remain host-owned until committed or interrupted.
func editQueued(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time) {
	attached.threadInput.reset()
	parts := attached.conversation.TakeWaiting()
	control.Compaction.CancelQueued(true)
	restoreDrafts(attached, questions, approvals, picker, control.Turn, clock, parts...)
}

// flushInput sends stacked input once nothing blocks it: unsent steers into
// the running turn, otherwise unsent steers, then queued input, as a new turn.
func (session *nativeSession) flushInput(attached *attachedPresentation, picker *composerPicker, clock func() time.Time, editor **composer.Editor, parent context.Context) error {
	attached.conversation.Unsent = pendingQuestionReplies(attached.conversation.Unsent)
	attached.conversation.Queued = pendingQuestionReplies(attached.conversation.Queued)
	if session.restoring != nil || !session.Client.AcceptsInput() {
		return nil
	}
	if len(attached.conversation.Unsent)+len(attached.conversation.Queued) > 0 {
		if ready, err := session.guardHookCheck.check(&session.Client, attached); !ready || err != nil {
			return err
		}
	}
	next := attached.presentation.NextInput(&session.Client.Live, attached.conversation, session.controlObservations, attached.cwd)
	if next.Interrupt {
		return applyInterruption(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, session.Client.Interrupt())
	}
	if len(next.Batch.Parts) == 0 && !next.Continue {
		return nil
	}
	childInput := attached.threadInput != nil && attached.threadInput.caller != "" && attached.threadInput.caller != "/root"
	compacting := len(next.Batch.Parts) == 1 && next.Batch.Parts[0].Compact != nil
	if !compacting && (!next.Steer || session.Client.LocalStop != "" || childInput) {
		ready, err := attached.threadInput.prepare(parent, session.Client.Thread, session.Client.Requests)
		if err != nil {
			restoreDrafts(attached, &session.questions, &session.approvals, picker, session.Client.Turn, clock, next.Batch.Parts...)
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return nil
		}
		if !ready {
			attached.conversation.ReturnBatch(next.Batch)
			return nil
		}
	}
	if picker.waitForSkillBindings(attached, clock, session.Client.Requests, next.Batch.Parts) {
		attached.conversation.ReturnBatch(next.Batch)
		picker.refreshPicker(attached, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment)
		return nil
	}
	return sendConversationInput(attached, &session.Client, picker, &session.questions, &session.approvals, clock, session.settings.Model, session.serviceTiers, session.skillEnvironment, session.ownedImages, &session.guardHookCheck, next.Batch.Parts, next.Steer)
}

func sendConversationInput(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, model string, tiers *inferencegateway.Tiers, environment []string, owned map[string]bool, hook *approvalHookCheck, parts []conversationmodel.Input, steer bool) error {
	defer attached.threadInput.reset()
	defer attached.presentation.PruneInput(&control.Live, attached.conversation)
	if len(parts) == 1 && parts[0].Compact != nil {
		intent := sessioncontrol.CompactIntent(*parts[0].Compact)
		intent.Continue = intent.Continue && control.LocalStop == ""
		control.Compaction.Begin(intent)
		attached.presentation.Status.SetText("Compacting context" + "…")
		return control.Request("thread/compact/start", map[string]any{"threadId": control.Thread})
	}
	for i := range parts {
		picker.bindSkills(attached.cwd, &parts[i], true)
		snapshotDraftSkills(attached.cwd, environment, &parts[i])
		parts[i].ReplyCount = len(binding.QuestionReplies(parts[i].Text))
	}
	s := conversationmodel.Submission{Draft: composer.Join(conversationmodel.Drafts(parts)...), Parts: parts}
	id := rand.Text()
	if admission := attached.threadInput; admission != nil && admission.ready {
		id = admission.id
		if admission.effect != nil {
			steer = admission.effect.Turn != ""
		}
	}
	input, err := prepareSubmissionInput(&s.Draft, id)
	if err != nil {
		restoreDrafts(attached, questions, approvals, picker, control.Turn, clock, parts...)
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	if len(parts) == 0 {
		input = []map[string]any{}
	}
	request := sessioncontrol.InputRequest{Thread: control.Thread, ID: id, Input: input}
	if steer {
		request.Turn = control.Turn
		if admission := attached.threadInput; admission != nil && admission.effect != nil {
			request.Turn = admission.effect.Turn
		}
	} else {
		request.Tier = selectedServiceTier(tiers, model, control.Thread)
		if len(parts) > 0 && len(binding.QuestionReplies(s.Text)) == 0 {
			echo := attached.source.LastSeq + 1
			attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: echo, Agent: "You", Kind: "text", Text: s.Text, Observed: presentationTime(clock),
				Native: &sessionpresentation.NativeItem{Thread: control.Thread, Item: "input/" + id, Phase: "input/pending", Spans: sessionpresentation.DraftSpans(s.Draft)}}}})
		}
	}
	for _, image := range s.Images {
		delete(owned, image.Path)
	}
	for _, path := range s.AnswerImages {
		delete(owned, path)
	}
	attached.presentation.StageInput(&control.Live, attached.conversation, id, s, steer)
	// Admission and skill acquisition can defer a selected continuation. Consume
	// it only once the input request has reached the ordinary submission owner.
	control.Compaction.Continue(len(parts) > 0)
	control.Shell.ConsumeContinuation()
	if !steer {
		hook.readyThread = ""
	}
	if s.AttachmentNotice != "" {
		notice := s.AttachmentNotice
		if strings.HasPrefix(attached.feedback.Text, "Skill attachment unavailable:") || strings.HasPrefix(attached.feedback.Text, "Skill catalog incomplete:") {
			notice = attached.feedback.Text + " " + notice
		}
		setNotice(&attached.feedback, presentationTime(clock), notice, true)
	}
	if admission := attached.threadInput; admission != nil && admission.effect != nil {
		effect, driver := admission.effect, admission.driver
		request.Settled = func(turn string, err error) { driver.HumanInputAcknowledged(effect.Target, effect.ID, turn, err) }
		admission.effect = nil // The host request owner now retains the outcome.
	}
	if _, err := control.Requests.SendInput(request, appServerRequest{}); err != nil {
		applyInputEffects(attached, questions, approvals, picker, control.Turn, clock, attached.presentation.ApplyInputResult(&control.Live, attached.conversation, control.SubmissionFailed()))
		return err
	}
	for _, part := range parts {
		if part.Question != nil {
			part.Question.SubmissionID = id
		}
	}
	return nil
}

func submissionResponse(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, method string, failure *appserver.Error) {
	message := ""
	if failure != nil {
		message = failure.Message
	}
	applyInputEffects(attached, questions, approvals, picker, control.Turn, clock, attached.presentation.AcknowledgeInput(&control.Live, attached.conversation, method, message, failure != nil))
}

func applyInputEffects(attached *attachedPresentation, questions *nativeQuestionDock, approvals *nativeApprovalDock, picker *composerPicker, turn string, clock func() time.Time, effects sessionpresentation.InputEffects) {
	for i, text := range effects.History {
		if h := attached.inputHistory; h != nil {
			history := attached.conversation.Editor.InputHistory
			if index := len(history) - len(effects.History) + i; index >= 0 {
				history[index].Source = h.thread
			}
			h.remember(text)
		}
	}
	if effects.Withdrawn != "" {
		attached.source.RemovePendingInput(effects.Withdrawn)
		attached.presentation.SettleWithdrawnInput(effects, len(attached.source.Model.Entries) > 0)
	}
	applyInputRestoration(attached, questions, approvals, picker, turn, clock, effects.Restored)
	if effects.Notice != "" {
		setNotice(&attached.feedback, presentationTime(clock), effects.Notice, effects.NoticeError)
	}
}

func commitSteer(attached *attachedPresentation, control *sessioncontrol.Live, item binding.ThreadItem) {
	text, _, _ := sessionpresentation.UserText(item.Content)
	attached.presentation.CommitInput(control, attached.conversation, item.ClientID, text)
}

func settleInput(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, turn string, interrupted bool) {
	for _, effects := range attached.presentation.SettleInput(&control.Live, attached.conversation, turn, interrupted) {
		applyInputEffects(attached, questions, approvals, picker, control.Turn, clock, effects)
	}
}
