package router

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"

	"github.com/yusing/mekugi/backends/codex/binding"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/appserver"
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

func (u *appServerUI) pruneInputPayloads() {
	u.conversation().Retain(append([]string{u.Submission().ID, u.PendingStart()}, u.Steers()...))
}

// takeDraft empties the composer. The caller stacks the draft, which keeps its
// image files until sent.
func (u *appServerUI) takeDraft() conversationmodel.Input {
	d := conversationmodel.Input{Draft: u.Editor.Take()}
	d.ReplyCount = len(binding.QuestionReplies(d.Text))
	u.bindSkills(&d, true)
	d.SnapshotFiles(u.session.cwd)
	u.snapshotDraftSkills(&d)
	u.cursorColumn = nil
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func (u *appServerUI) restoreDrafts(parts ...conversationmodel.Input) {
	parts = u.rejectQuestionParts(parts)
	if u.approvals.Open && u.pendingApprovals()[0].DenialChoice() >= 0 {
		u.hideApprovals()
		u.restoreDrafts(parts...)
		u.openApprovals()
		return
	}
	if u.activeQuestionCall() != nil {
		c, index := u.activeQuestionCall(), u.questions.Index
		u.hideQuestions()
		u.restoreDrafts(parts...)
		u.promptColumn = u.cursorColumn
		u.promptEditor.Open(&u.Editor, composer.State{})
		u.questions.Active, u.questions.Index = &c.Call, index
		u.resumeQuestion(u.currentQuestion())
		return
	}
	if len(parts) == 0 {
		return
	}
	u.Editor.Restore(conversationmodel.Drafts(parts)...)
	u.cursorColumn = nil
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
	compactSteer := len(u.conversation().Unsent) > 0 && u.conversation().Unsent[0].Text == "/compact" && !u.conversation().Unsent[0].Compact.WaitForIdle
	if u.waitingQuestion() && !compactSteer {
		return nil
	}
	if u.Turn == "" {
		if text := u.Compaction.Continue(len(u.conversation().Unsent)+len(u.conversation().Queued) != 0); text != "" {
			u.conversation().Unsent = append(u.conversation().Unsent, conversationmodel.Input{Text: text})
		}
	}
	var parts []conversationmodel.Input
	fromUnsent := len(u.conversation().Unsent) > 0
	steer := u.Turn != ""
	switch {
	case len(u.conversation().Unsent) > 0:
		// Answers must not preempt an unfinished tool input. Once input is
		// complete, steer normally, even if the host command is still running.
		if steer && u.conversation().Unsent[0].Question != nil && u.proxy.emittingToolInput(u.session.cwd, u.Thread) {
			// Explicit prompts retain instant interruption even when an answer
			// ahead of them is waiting for generation to finish.
			if !u.conversation().AdvancePrompt() {
				return nil
			}
		}
		if steer && u.conversation().Unsent[0].Text == "/compact" {
			if u.Compaction.InterruptBefore(sessioncontrol.CompactIntent(u.conversation().Unsent[0].Compact), u.Turn) {
				return u.interruptTurn()
			}
			return nil
		}
		parts, u.conversation().Unsent = conversationmodel.Batch(u.conversation().Unsent)
	case !steer && len(u.conversation().Queued) > 0:
		parts, u.conversation().Queued = conversationmodel.Batch(u.conversation().Queued)
	default:
		return nil
	}
	if u.waitForSkillBindings(parts) {
		if fromUnsent {
			u.conversation().Unsent = append(parts, u.conversation().Unsent...)
		} else {
			u.conversation().Queued = append(parts, u.conversation().Queued...)
		}
		u.refreshPicker()
		return nil
	}
	return u.send(parts, steer)
}

func (u *appServerUI) send(parts []conversationmodel.Input, steer bool) error {
	defer u.pruneInputPayloads()
	if len(parts) == 1 && parts[0].Text == "/compact" {
		u.Compaction.Begin(sessioncontrol.CompactIntent(parts[0].Compact))
		u.sessionPresentation().Status.SetText("Compacting context" + "…")
		return u.request("thread/compact/start", map[string]any{"threadId": u.Thread})
	}
	for i := range parts {
		u.bindSkills(&parts[i], true)
		u.snapshotDraftSkills(&parts[i])
	}
	s := conversationmodel.Submission{Draft: composer.Join(conversationmodel.Drafts(parts)...), Parts: parts}
	id, turn := rand.Text(), ""
	input := conversationbinding.Input(s.Draft)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(s.Attachments) > 0 || len(s.Skills) > 0) && textBytes > composerTextLimit {
		u.restoreDrafts(parts...)
		u.setNotice("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.", true)
		return nil
	}
	params := map[string]any{"threadId": u.Thread, "input": input, "clientUserMessageId": id}
	method := "turn/start"
	if steer {
		// The pending-input preview shows a steer until Codex commits it.
		method, turn = "turn/steer", u.Turn
		params["expectedTurnId"] = u.Turn
	} else {
		if tier := u.selectedServiceTier(); tier != "" {
			params["serviceTier"] = tier
		}
		if len(binding.QuestionReplies(s.Text)) == 0 {
			s.Echo = u.view.LastSeq + 1
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: s.Echo, Agent: "You", Kind: "text", Text: s.Text, Observed: u.now(),
				native: &liveActivityNativeItem{thread: u.Thread, item: fmt.Sprintf("input/%d", s.Echo), phase: "input/pending", spans: draftDisplaySpans(s.Draft)}}}})
		}
	}
	for _, image := range s.Images {
		delete(u.ownedImages, image.Path)
	}
	for _, path := range s.AnswerImages {
		delete(u.ownedImages, path)
	}
	u.conversation().Stage(id, s)
	u.Submit(id, turn)
	if !steer {
		u.guardHookCheck.readyThread = ""
	}
	u.sessionPresentation().Status.Set("Sending…", false)
	if s.AttachmentNotice != "" {
		notice := s.AttachmentNotice
		if strings.HasPrefix(u.feedback.Text, "Skill attachment unavailable:") || strings.HasPrefix(u.feedback.Text, "Skill catalog incomplete:") {
			notice = u.feedback.Text + " " + notice
		}
		u.setNotice(notice, true)
	}
	if err := u.request(method, params); err != nil {
		u.applyInputResult(u.SubmissionFailed())
		return err
	}
	for _, part := range parts {
		if part.Question != nil {
			part.Question.SubmissionID = id
		}
	}
	return nil
}

// withdraw returns a rejected or interrupted submission to the composer.
func (u *appServerUI) withdraw(result sessioncontrol.InputResult) {
	s := u.conversation().Submitted[result.Submission.ID]
	if result.Submission.Turn != "" {
		u.restoreDrafts(s.Parts...)
		return
	}
	u.view.removePendingInput(s.Echo)
	if result.Submission.Interrupted && len(u.view.Entries) == 0 {
		u.sessionPresentation().Status.Started = time.Time{}
		u.sessionPresentation().Status.SetText("Ready")
	}
	if result.KeepWaiting {
		u.restoreDrafts(s.Parts...)
		return
	}
	u.restoreDrafts(slices.Concat(s.Parts, u.conversation().Unsent, u.conversation().Queued)...)
	u.conversation().Unsent, u.conversation().Queued = nil, nil
}

func (u *appServerUI) submissionResponse(method string, failure *appserver.Error) {
	defer u.pruneInputPayloads()
	id := u.Submission().ID
	s := u.conversation().Submitted[id]
	if failure == nil {
		for i, part := range s.Parts {
			if !part.InHistory {
				u.rememberInput(part)
				s.Parts[i].InHistory = true
			}
		}
	}
	result := u.AcknowledgeInput(failure != nil, len(s.Parts) == 1 && s.Parts[0].Question != nil, len(u.conversation().Unsent) > 0)
	if result.Action == sessioncontrol.InputRejected {
		u.sessionPresentation().Status.Set(method+": "+failure.Message, true)
	}
	u.applyInputResult(result)
}

func (u *appServerUI) applyInputResult(result sessioncontrol.InputResult) {
	switch result.Action {
	case sessioncontrol.InputAccepted, sessioncontrol.InputStale:
		if u.sessionPresentation().Status.Text == "Sending…" && !u.sessionPresentation().Status.Alert {
			if u.Turn != "" {
				u.sessionPresentation().Status.SetText("Working")
			} else if u.Starting() {
				u.sessionPresentation().Status.SetText("Starting turn…")
			}
		}
		if result.Action != sessioncontrol.InputStale {
			return
		}
		u.restoreDrafts(u.conversation().Submitted[result.Submission.ID].Parts...)
		u.setNotice("Turn ended before the steer · restored to the composer", false)
	case sessioncontrol.InputRejected, sessioncontrol.InputWithdrawn:
		u.withdraw(result)
	case sessioncontrol.InputSettled:
		u.applySteerResult(result.Steers)
	}
}

// Host messages without a client ID retain payload matching at the composer.
// Only the resulting canonical identity crosses into session control.
func (u *appServerUI) commitSteer(item binding.ThreadItem) {
	defer u.pruneInputPayloads()
	if item.ClientID != "" {
		u.CommitInput(item.ClientID)
		return
	}
	text, _, _ := appServerUserText(item.Content)
	if id := u.PendingStart(); id != "" && u.conversation().Submitted[id].Text == text {
		u.CommitInput(id)
	}
	for _, id := range append([]string{u.Submission().ID}, u.Steers()...) {
		if id != "" && u.conversation().Submitted[id].Text == text {
			u.CommitInput(id)
			return
		}
	}
}

func (u *appServerUI) settleInput(turn string, interrupted bool) {
	defer u.pruneInputPayloads()
	for _, result := range u.SettleInput(turn, interrupted, len(u.conversation().Unsent) > 0) {
		u.applyInputResult(result)
	}
}

func (u *appServerUI) applySteerResult(result sessioncontrol.SteerResult) {
	steers := u.conversation().Parts(result.IDs)
	switch result.Action {
	case sessioncontrol.SteersRestore:
		u.restoreDrafts(steers...)
	case sessioncontrol.SteersRequeue:
		u.conversation().Unsent = slices.Concat(steers, u.conversation().Unsent)
	case sessioncontrol.SteersRestoreAll:
		u.restoreDrafts(slices.Concat(steers, u.conversation().Unsent, u.conversation().Queued)...)
		u.conversation().Unsent, u.conversation().Queued = nil, nil
	case sessioncontrol.SteersUnconfirmed:
		u.restoreDrafts(steers...)
		u.setNotice("Unconfirmed steer restored to the composer", false)
	}
}

func (u *appServerUI) pendingInputPreview(width int) []string {
	ids := slices.Clone(u.Steers())
	if u.Submission().Turn != "" {
		ids = append(ids, u.Submission().ID)
	}
	reason := conversationmodel.Waiting
	switch {
	case u.Compaction.PreservesWaitingInput():
		reason = conversationmodel.CompactAfterInterrupt
	case u.Shell.Standalone():
		reason = conversationmodel.ShellRunning
	case u.SendSteersAfterInterrupt || u.SteerInterruptAckPending:
		reason = conversationmodel.SendingAfterInterrupt
	case u.Interruption.Target != "":
		reason = conversationmodel.RestoringAfterInterrupt
	case u.Compaction.Active():
		reason = conversationmodel.CompactRunning
	case len(u.conversation().Unsent) > 0 && u.conversation().Unsent[0].Text == "/compact":
		reason = conversationmodel.CompactQueued
	case u.Turn != "" || u.Starting():
		reason = conversationmodel.Steering
	case u.Settings.Pending() || u.Settings.RestoreEffort:
		reason = conversationmodel.ApplyingSettings
	}
	return conversationui.Pending(u.conversation().Pending(ids, reason), width)
}
