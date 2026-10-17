package router

import (
	"crypto/rand"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
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

func (u *appServerUI) conversation() *conversationmodel.Session {
	return u.conversationState.Session(u.session.Scope())
}

func (u *appServerUI) bindConversation() {
	m := u.conversation()
	if u.Editor != &u.secretEditor {
		u.Editor = &m.Editor
	}
	u.promptEditor = &m.PromptEditor
	u.questions.Questions, u.approvals.Approvals = &m.Questions, &m.Approvals
}

// takeDraft empties the composer. The caller stacks the draft, which keeps its
// image files until sent.
func (u *appServerUI) takeDraft() conversationmodel.Input {
	return u.prepareDraft(u.Editor.Take())
}

func (u *appServerUI) prepareDraft(draft composer.Draft) conversationmodel.Input {
	d := conversationmodel.Input{Draft: draft}
	d.ReplyCount = len(binding.QuestionReplies(d.Text))
	u.bindSkills(&d, true)
	d.SnapshotFiles(u.session.cwd)
	u.snapshotDraftSkills(&d)
	u.mainView.CursorColumn = nil
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func (u *appServerUI) restoreDrafts(parts ...conversationmodel.Input) {
	u.applyInputRestoration(u.conversation().RestoreInput(parts...))
}

func (u *appServerUI) applyInputRestoration(restored conversationmodel.InputRestoration) {
	for _, id := range restored.Rejected {
		for _, call := range u.questionCalls() {
			if call.ID == id {
				u.renderQuestionRecord(call)
				break
			}
		}
	}
	if restored.Parked {
		if restored.Changed {
			u.mainView.PromptColumn = nil
		}
		if u.approvals.Open {
			u.approvals.painted = false
			u.focusPrompt()
		}
	} else if restored.Changed {
		u.mainView.CursorColumn = nil
	}
}

// editQueued returns all locally waiting input to the composer as one stack.
// Already-sent steers remain host-owned until committed or interrupted.
func (u *appServerUI) editQueued() {
	parts := u.conversation().TakeWaiting()
	u.Compaction.CancelQueued(true)
	u.restoreDrafts(parts...)
}

// flushInput sends stacked input once nothing blocks it: unsent steers into
// the running turn, otherwise unsent steers, then queued input, as a new turn.
func (u *appServerUI) flushInput() error {
	u.conversation().Unsent = pendingQuestionReplies(u.conversation().Unsent)
	u.conversation().Queued = pendingQuestionReplies(u.conversation().Queued)
	if u.restoring != nil || !u.AcceptsInput() {
		return nil
	}
	if len(u.conversation().Unsent)+len(u.conversation().Queued) > 0 {
		if ready, err := u.checkGuardHook(); !ready || err != nil {
			return err
		}
	}
	emitting := u.controlObservations != nil && u.controlObservations.EmittingToolInput(u.session.cwd, u.Thread)
	next := u.sessionPresentation().NextInput(&u.Live, u.conversation(), emitting)
	if next.Interrupt {
		return u.interruptTurn()
	}
	if len(next.Batch.Parts) == 0 {
		return nil
	}
	if u.waitForSkillBindings(next.Batch.Parts) {
		u.conversation().ReturnBatch(next.Batch)
		u.refreshPicker()
		return nil
	}
	return u.send(next.Batch.Parts, next.Steer)
}

func (u *appServerUI) send(parts []conversationmodel.Input, steer bool) error {
	defer u.sessionPresentation().PruneInput(&u.Live, u.conversation())
	if len(parts) == 1 && parts[0].Text == "/compact" {
		u.Compaction.Begin(sessioncontrol.CompactIntent(parts[0].Compact))
		u.sessionPresentation().Status.SetText("Compacting context" + "…")
		return u.request("thread/compact/start", map[string]any{"threadId": u.Thread})
	}
	for i := range parts {
		u.bindSkills(&parts[i], true)
		u.snapshotDraftSkills(&parts[i])
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
		u.setNotice("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.", true)
		return nil
	}
	request := sessioncontrol.InputRequest{Thread: u.Thread, ID: id, Input: input}
	if steer {
		request.Turn = u.Turn
	} else {
		request.Tier = u.selectedServiceTier()
		if len(binding.QuestionReplies(s.Text)) == 0 {
			echo := u.view.LastSeq + 1
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: echo, Agent: "You", Kind: "text", Text: s.Text, Observed: u.now(),
				native: &liveActivityNativeItem{thread: u.Thread, item: "input/" + id, phase: "input/pending", spans: terminalsession.DraftSpans(s.Draft)}}}})
		}
	}
	for _, image := range s.Images {
		delete(u.ownedImages, image.Path)
	}
	for _, path := range s.AnswerImages {
		delete(u.ownedImages, path)
	}
	u.sessionPresentation().StageInput(&u.Live, u.conversation(), id, s, steer)
	if !steer {
		u.guardHookCheck.readyThread = ""
	}
	if s.AttachmentNotice != "" {
		notice := s.AttachmentNotice
		if strings.HasPrefix(u.feedback.Text, "Skill attachment unavailable:") || strings.HasPrefix(u.feedback.Text, "Skill catalog incomplete:") {
			notice = u.feedback.Text + " " + notice
		}
		u.setNotice(notice, true)
	}
	if _, err := u.requests.SendInput(request, appServerRequest{}); err != nil {
		u.applyInputEffects(u.sessionPresentation().ApplyInputResult(&u.Live, u.conversation(), u.SubmissionFailed()))
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
	u.applyInputEffects(u.sessionPresentation().AcknowledgeInput(&u.Live, u.conversation(), method, message, failure != nil))
}

func (u *appServerUI) applyInputEffects(effects sessionpresentation.InputEffects) {
	if effects.Withdrawn != "" {
		u.view.removePendingInput(effects.Withdrawn)
		u.sessionPresentation().SettleWithdrawnInput(effects, len(u.view.Model.Entries) > 0)
	}
	u.applyInputRestoration(effects.Restored)
	if effects.Notice != "" {
		u.setNotice(effects.Notice, effects.NoticeError)
	}
}

func (u *appServerUI) commitSteer(item binding.ThreadItem) {
	text, _, _ := appServerUserText(item.Content)
	u.sessionPresentation().CommitInput(&u.Live, u.conversation(), item.ClientID, text)
}

func (u *appServerUI) settleInput(turn string, interrupted bool) {
	for _, effects := range u.sessionPresentation().SettleInput(&u.Live, u.conversation(), turn, interrupted) {
		u.applyInputEffects(effects)
	}
}
