package router

import (
	"crypto/rand"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
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

func prepareDraft(picker *composerPicker, terminal *terminalsession.Attachment, workspace string, environment []string, draft composer.Draft) conversationmodel.Input {
	d := conversationmodel.Input{Draft: draft}
	d.ReplyCount = len(binding.QuestionReplies(d.Text))
	picker.bindSkills(workspace, &d, true)
	d.SnapshotFiles(workspace)
	snapshotDraftSkills(workspace, environment, &d)
	terminal.Main.CursorColumn = nil
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func (u *appServerUI) restoreDrafts(parts ...conversationmodel.Input) {
	u.applyInputRestoration(u.attached.conversation.RestoreInput(parts...))
}

func (u *appServerUI) applyInputRestoration(restored conversationmodel.InputRestoration) {
	for _, id := range restored.Rejected {
		for _, call := range u.questions.calls() {
			if call.ID == id {
				renderQuestionRecord(&u.attached, u.Turn, presentationTime(u.clock), call)
				break
			}
		}
	}
	if restored.Parked {
		if restored.Changed {
			u.shell.Main.PromptColumn = nil
		}
		if u.approvals.Open {
			u.shell.Prompts.Reopen()
			focusPrompt(&u.picker, u.shell)
		}
	} else if restored.Changed {
		u.shell.Main.CursorColumn = nil
	}
}

// editQueued returns all locally waiting input to the composer as one stack.
// Already-sent steers remain host-owned until committed or interrupted.
func (u *appServerUI) editQueued() {
	parts := u.attached.conversation.TakeWaiting()
	u.Compaction.CancelQueued(true)
	u.restoreDrafts(parts...)
}

// flushInput sends stacked input once nothing blocks it: unsent steers into
// the running turn, otherwise unsent steers, then queued input, as a new turn.
func (u *appServerUI) flushInput() error {
	u.attached.conversation.Unsent = pendingQuestionReplies(u.attached.conversation.Unsent)
	u.attached.conversation.Queued = pendingQuestionReplies(u.attached.conversation.Queued)
	if u.restoring != nil || !u.AcceptsInput() {
		return nil
	}
	if len(u.attached.conversation.Unsent)+len(u.attached.conversation.Queued) > 0 {
		if ready, err := u.guardHookCheck.check(&u.Client, &u.attached); !ready || err != nil {
			return err
		}
	}
	emitting := u.controlObservations != nil && u.controlObservations.EmittingToolInput(u.attached.cwd, u.Thread)
	next := u.attached.presentation.NextInput(&u.Live, u.attached.conversation, emitting)
	if next.Interrupt {
		return u.interruptTurn()
	}
	if len(next.Batch.Parts) == 0 {
		return nil
	}
	if u.picker.waitForSkillBindings(&u.attached, u.shell, u.clock, u.Requests, next.Batch.Parts) {
		u.attached.conversation.ReturnBatch(next.Batch)
		u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
		return nil
	}
	return u.send(next.Batch.Parts, next.Steer)
}

func (u *appServerUI) send(parts []conversationmodel.Input, steer bool) error {
	defer u.attached.presentation.PruneInput(&u.Live, u.attached.conversation)
	if len(parts) == 1 && parts[0].Text == "/compact" {
		u.Compaction.Begin(sessioncontrol.CompactIntent(parts[0].Compact))
		u.attached.presentation.Status.SetText("Compacting context" + "…")
		return u.Request("thread/compact/start", map[string]any{"threadId": u.Thread})
	}
	for i := range parts {
		u.picker.bindSkills(u.attached.cwd, &parts[i], true)
		snapshotDraftSkills(u.attached.cwd, u.skillEnvironment, &parts[i])
		parts[i].ReplyCount = len(binding.QuestionReplies(parts[i].Text))
	}
	s := conversationmodel.Submission{Draft: composer.Join(conversationmodel.Drafts(parts)...), Parts: parts}
	id := rand.Text()
	input := conversationbinding.Input(s.Draft)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(s.Attachments) > 0 || len(s.Skills) > 0) && textBytes > codexresponses.UserTextLimit {
		u.restoreDrafts(parts...)
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.", true)
		return nil
	}
	request := sessioncontrol.InputRequest{Thread: u.Thread, ID: id, Input: input}
	if steer {
		request.Turn = u.Turn
	} else {
		request.Tier = selectedServiceTier(u.serviceTiers, u.model, u.Thread)
		if len(binding.QuestionReplies(s.Text)) == 0 {
			echo := u.attached.source.LastSeq + 1
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: echo, Agent: "You", Kind: "text", Text: s.Text, Observed: presentationTime(u.clock),
				Native: &sessionpresentation.NativeItem{Thread: u.Thread, Item: "input/" + id, Phase: "input/pending", Spans: sessionpresentation.DraftSpans(s.Draft)}}}})
		}
	}
	for _, image := range s.Images {
		delete(u.ownedImages, image.Path)
	}
	for _, path := range s.AnswerImages {
		delete(u.ownedImages, path)
	}
	u.attached.presentation.StageInput(&u.Live, u.attached.conversation, id, s, steer)
	if !steer {
		u.guardHookCheck.readyThread = ""
	}
	if s.AttachmentNotice != "" {
		notice := s.AttachmentNotice
		if strings.HasPrefix(u.attached.feedback.Text, "Skill attachment unavailable:") || strings.HasPrefix(u.attached.feedback.Text, "Skill catalog incomplete:") {
			notice = u.attached.feedback.Text + " " + notice
		}
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), notice, true)
	}
	if _, err := u.Requests.SendInput(request, appServerRequest{}); err != nil {
		u.applyInputEffects(u.attached.presentation.ApplyInputResult(&u.Live, u.attached.conversation, u.SubmissionFailed()))
		return err
	}
	for _, part := range parts {
		if part.Question != nil {
			part.Question.SubmissionID = id
		}
	}
	return nil
}

func (u *appServerUI) submissionResponse(method string, failure *appserver.Error) {
	message := ""
	if failure != nil {
		message = failure.Message
	}
	u.applyInputEffects(u.attached.presentation.AcknowledgeInput(&u.Live, u.attached.conversation, method, message, failure != nil))
}

func (u *appServerUI) applyInputEffects(effects sessionpresentation.InputEffects) {
	if effects.Withdrawn != "" {
		u.attached.source.RemovePendingInput(effects.Withdrawn)
		u.attached.presentation.SettleWithdrawnInput(effects, len(u.attached.source.Model.Entries) > 0)
	}
	u.applyInputRestoration(effects.Restored)
	if effects.Notice != "" {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), effects.Notice, effects.NoticeError)
	}
}

func commitSteer(attached *attachedPresentation, control *sessioncontrol.Live, item binding.ThreadItem) {
	text, _, _ := sessionpresentation.UserText(item.Content)
	attached.presentation.CommitInput(control, attached.conversation, item.ClientID, text)
}

func (u *appServerUI) settleInput(turn string, interrupted bool) {
	for _, effects := range u.attached.presentation.SettleInput(&u.Live, u.attached.conversation, turn, interrupted) {
		u.applyInputEffects(effects)
	}
}
