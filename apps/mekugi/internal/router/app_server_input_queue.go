package router

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Busy input follows Codex's composer queue: Enter steers the running turn,
// Tab queues input for the next turn. Ctrl-C restores uncommitted input;
// Escape expedites pending steers after interrupting the turn. Unlike Codex,
// waiting input stacks:
// unsent steers, and separately queued messages, go out as one message joined
// by newlines instead of one message per turn.
// Source: codex-rs/tui/src/chatwidget/{input_queue,input_flow,input_restore}.rs
// @86be5320b068ef67b56348b02aa8c33706955da6.

// composerSubmission retains only payload and presentation. Session control
// owns acknowledgement, commitment and ordering by client submission identity.
type composerSubmission struct {
	composerDraft
	parts []composerDraft // The composer entries it joins, for input history.
	seq   uint64          // The transcript echo of a turn/start.
}

func draftValues(parts []composerDraft) []composer.Draft {
	values := make([]composer.Draft, len(parts))
	for i := range parts {
		values[i] = parts[i].Draft
	}
	return values
}
func joinDrafts(parts ...composerDraft) composerDraft {
	return composerDraft{Draft: composer.Join(draftValues(parts)...)}
}

func (u *appServerUI) steerParts(ids []string) []composerDraft {
	var parts []composerDraft
	for _, id := range ids {
		parts = append(parts, u.inputPayloads[id].parts...)
	}
	return parts
}

func (u *appServerUI) pruneInputPayloads() {
	retained := make(map[string]bool, len(u.Steers())+2)
	retained[u.Submission().ID], retained[u.PendingStart()] = true, true
	for _, id := range u.Steers() {
		retained[id] = true
	}
	for id := range u.inputPayloads {
		if !retained[id] {
			delete(u.inputPayloads, id)
		}
	}
}

// takeDraft empties the composer. The caller stacks the draft, which keeps its
// image files until sent.
func (u *appServerUI) takeDraft() composerDraft {
	d := composerDraft{Draft: u.Editor.Take()}
	u.bindSkills(&d, true)
	d.SnapshotFiles(u.session.cwd)
	u.snapshotDraftSkills(&d)
	u.cursorColumn = nil
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func (u *appServerUI) restoreDrafts(parts ...composerDraft) {
	parts = u.rejectQuestionParts(parts)
	if u.approvals.open && u.approvals.pending[0].denialChoice() >= 0 {
		u.hideApprovals()
		u.restoreDrafts(parts...)
		u.openApprovals()
		return
	}
	if u.questions.active != nil {
		c, index := u.questions.active, u.questions.index
		u.hideQuestions()
		u.restoreDrafts(parts...)
		u.questions.parked = u.saveQuestionEditor()
		u.questions.active, u.questions.index = c, index
		u.loadQuestionEditor(u.currentQuestion().editor)
		return
	}
	if len(parts) == 0 {
		return
	}
	u.Editor.Restore(draftValues(parts)...)
	u.cursorColumn = nil
}

// editQueued returns all locally waiting input to the composer as one stack.
// Already-sent steers remain host-owned until committed or interrupted.
func (u *appServerUI) editQueued() {
	parts := slices.Concat(u.unsent, u.queued)
	u.unsent, u.queued = nil, nil
	u.Compaction.CancelQueued(true)
	u.restoreDrafts(parts...)
}

// flushInput sends stacked input once nothing blocks it: unsent steers into
// the running turn, otherwise unsent steers, then queued input, as a new turn.
func (u *appServerUI) flushInput() error {
	u.unsent = pendingQuestionReplies(u.unsent)
	u.queued = pendingQuestionReplies(u.queued)
	if u.restoring != nil || !u.AcceptsInput() {
		return nil
	}
	if len(u.unsent)+len(u.queued) > 0 {
		if ready, err := u.checkGuardHook(); !ready || err != nil {
			return err
		}
	}
	compactSteer := len(u.unsent) > 0 && u.unsent[0].Text == "/compact" && !u.unsent[0].compact.WaitForIdle
	if u.waitingQuestion() && !compactSteer {
		return nil
	}
	if u.Turn == "" {
		if text := u.Compaction.Continue(len(u.unsent)+len(u.queued) != 0); text != "" {
			u.unsent = append(u.unsent, composerDraft{Text: text})
		}
	}
	var parts []composerDraft
	fromUnsent := len(u.unsent) > 0
	steer := u.Turn != ""
	switch {
	case len(u.unsent) > 0:
		// Answers must not preempt an unfinished tool input. Once input is
		// complete, steer normally, even if the host command is still running.
		if steer && u.unsent[0].questionCall != nil && u.proxy.emittingToolInput(u.session.cwd, u.Thread) {
			// Explicit prompts retain instant interruption even when an answer
			// ahead of them is waiting for generation to finish.
			first := 0
			for first < len(u.unsent) && u.unsent[first].questionCall != nil {
				first++
			}
			if first == len(u.unsent) {
				return nil
			}
			prompt, rest := questionSubmissionBatch(u.unsent[first:])
			u.unsent = slices.Concat(prompt, u.unsent[:first], rest)
		}
		if steer && u.unsent[0].Text == "/compact" {
			if u.Compaction.InterruptBefore(u.unsent[0].compact, u.Turn) {
				return u.interruptTurn()
			}
			return nil
		}
		parts, u.unsent = questionSubmissionBatch(u.unsent)
	case !steer && len(u.queued) > 0:
		parts, u.queued = questionSubmissionBatch(u.queued)
	default:
		return nil
	}
	if u.waitForSkillBindings(parts) {
		if fromUnsent {
			u.unsent = append(parts, u.unsent...)
		} else {
			u.queued = append(parts, u.queued...)
		}
		u.refreshPicker()
		return nil
	}
	return u.send(parts, steer)
}

func (u *appServerUI) send(parts []composerDraft, steer bool) error {
	defer u.pruneInputPayloads()
	if len(parts) == 1 && parts[0].Text == "/compact" {
		u.Compaction.Begin(parts[0].compact)
		u.status = "Compacting context" + "…"
		return u.request("thread/compact/start", map[string]any{"threadId": u.Thread})
	}
	for i := range parts {
		u.bindSkills(&parts[i], true)
		u.snapshotDraftSkills(&parts[i])
	}
	s := composerSubmission{composerDraft: joinDrafts(parts...), parts: parts}
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
		if len(questionReplies(s.Text)) == 0 {
			s.seq = u.view.LastSeq + 1
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: s.seq, Agent: "You", Kind: "text", Text: s.Text, Observed: u.now(),
				native: &liveActivityNativeItem{thread: u.Thread, item: fmt.Sprintf("input/%d", s.seq), phase: "input/pending", spans: s.composerDraft.displaySpans()}}}})
		}
	}
	for _, image := range s.Images {
		delete(u.ownedImages, image.Path)
	}
	for _, path := range s.AnswerImages {
		delete(u.ownedImages, path)
	}
	if u.inputPayloads == nil {
		u.inputPayloads = make(map[string]composerSubmission)
	}
	u.inputPayloads[id] = s
	u.Submit(id, turn)
	if !steer {
		u.guardHookCheck.readyThread = ""
	}
	u.status, u.alert = "Sending…", false
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
		if part.questionCall != nil {
			part.questionCall.submissionID = id
		}
	}
	return nil
}

// withdraw returns a rejected or interrupted submission to the composer.
func (u *appServerUI) withdraw(result sessioncontrol.InputResult) {
	s := u.inputPayloads[result.Submission.ID]
	if result.Submission.Turn != "" {
		u.restoreDrafts(s.parts...)
		return
	}
	u.view.removePendingInput(s.seq)
	if result.Submission.Interrupted && len(u.view.Entries) == 0 {
		u.status, u.turnStarted = "Ready", time.Time{}
	}
	if result.KeepWaiting {
		u.restoreDrafts(s.parts...)
		return
	}
	u.restoreDrafts(slices.Concat(s.parts, u.unsent, u.queued)...)
	u.unsent, u.queued = nil, nil
}

func (u *appServerUI) submissionResponse(method string, failure *appserver.Error) {
	defer u.pruneInputPayloads()
	id := u.Submission().ID
	s := u.inputPayloads[id]
	if failure == nil {
		for i, part := range s.parts {
			if !part.InHistory {
				u.rememberInput(part)
				s.parts[i].InHistory = true
			}
		}
	}
	result := u.AcknowledgeInput(failure != nil, len(s.parts) == 1 && s.parts[0].questionCall != nil, len(u.unsent) > 0)
	if result.Action == sessioncontrol.InputRejected {
		u.status, u.alert = method+": "+failure.Message, true
	}
	u.applyInputResult(result)
}

func (u *appServerUI) applyInputResult(result sessioncontrol.InputResult) {
	switch result.Action {
	case sessioncontrol.InputAccepted, sessioncontrol.InputStale:
		if u.status == "Sending…" && !u.alert {
			if u.Turn != "" {
				u.status = "Working"
			} else if u.Starting() {
				u.status = "Starting turn…"
			}
		}
		if result.Action != sessioncontrol.InputStale {
			return
		}
		u.restoreDrafts(u.inputPayloads[result.Submission.ID].parts...)
		u.setNotice("Turn ended before the steer · restored to the composer", false)
	case sessioncontrol.InputRejected, sessioncontrol.InputWithdrawn:
		u.withdraw(result)
	case sessioncontrol.InputSettled:
		u.applySteerResult(result.Steers)
	}
}

// Host messages without a client ID retain payload matching at the composer.
// Only the resulting canonical identity crosses into session control.
func (u *appServerUI) commitSteer(item appServerItem) {
	defer u.pruneInputPayloads()
	if item.ClientID != "" {
		u.CommitInput(item.ClientID)
		return
	}
	text, _, _ := appServerUserText(item.Content)
	if id := u.PendingStart(); id != "" && u.inputPayloads[id].Text == text {
		u.CommitInput(id)
	}
	for _, id := range append([]string{u.Submission().ID}, u.Steers()...) {
		if id != "" && u.inputPayloads[id].Text == text {
			u.CommitInput(id)
			return
		}
	}
}

func (u *appServerUI) settleInput(turn string, interrupted bool) {
	defer u.pruneInputPayloads()
	for _, result := range u.SettleInput(turn, interrupted, len(u.unsent) > 0) {
		u.applyInputResult(result)
	}
}

func (u *appServerUI) applySteerResult(result sessioncontrol.SteerResult) {
	steers := u.steerParts(result.IDs)
	switch result.Action {
	case sessioncontrol.SteersRestore:
		u.restoreDrafts(steers...)
	case sessioncontrol.SteersRequeue:
		u.unsent = slices.Concat(steers, u.unsent)
	case sessioncontrol.SteersRestoreAll:
		u.restoreDrafts(slices.Concat(steers, u.unsent, u.queued)...)
		u.unsent, u.queued = nil, nil
	case sessioncontrol.SteersUnconfirmed:
		u.restoreDrafts(steers...)
		u.setNotice("Unconfirmed steer restored to the composer", false)
	}
}

// pendingInputPreview lists waiting input above the composer, like Codex's
// pending input preview: at most three rows per stack.
// Source: codex-rs/tui/src/bottom_pane/pending_input_preview.rs@86be5320.
func (u *appServerUI) pendingInputPreview(width int) []string {
	var lines []string
	section := func(header string, parts []composerDraft) {
		if len(parts) == 0 {
			return
		}
		lines = append(lines, ansi.Truncate(terminalui.Dim+"• "+terminalui.Undim+header, width, "…")+terminalui.Reset)
		var texts []string
		for _, part := range parts {
			text := part.Text
			if replies := questionReplies(text); len(replies) > 0 {
				text = fmt.Sprintf("Answering %d question(s)", len(replies))
			}
			texts = append(texts, text)
		}
		rows := strings.Split(textsanitize.Safe(strings.Join(texts, "\n"), false), "\n")
		for i, row := range rows[:min(3, len(rows))] {
			prefix := "    "
			if i == 0 {
				prefix = "  ↳ "
			}
			lines = append(lines, ansi.Truncate(terminalui.Dim+prefix+row, width, "…")+terminalui.Reset)
		}
		if len(rows) > 3 {
			lines = append(lines, terminalui.Dim+ansi.Truncate("    …", width, "")+terminalui.Reset)
		}
	}
	steers := u.steerParts(u.Steers())
	if u.Submission().Turn != "" {
		steers = append(steers, u.inputPayloads[u.Submission().ID].parts...)
	}
	header := "Steering after the next tool call · esc sends now · ctrl+c interrupts and restores input"
	switch {
	case u.Compaction.PreservesWaitingInput():
		header = "Compacting after interruption · ctrl+c cancels compaction"
	case u.Shell.Standalone():
		header = "Waiting for shell command to finish"
	case u.SendSteersAfterInterrupt || u.SteerInterruptAckPending:
		header = "Sending steer once interrupted · ctrl+c restores input"
	case u.Interruption.Target != "":
		header = "Restoring input once interrupted"
	case u.Compaction.Active():
		header = "Waiting for context compaction"
	case len(u.unsent) > 0 && u.unsent[0].Text == "/compact":
		header = "Compaction queued after this turn · ctrl+c cancels; Main continues"
	case u.Turn != "" || u.Starting():
	case u.Settings.Pending() || u.Settings.RestoreEffort:
		header = "Sending when settings apply"
	default:
		header = "Waiting to send"
	}
	section(header, append(steers, u.unsent...))
	section("Queued for the next turn · alt+↑ edits", u.queued)
	return lines
}

// A tool call's reply is one submission, never joined with another call or prompt.
func questionSubmissionBatch(stack []composerDraft) ([]composerDraft, []composerDraft) {
	n := 1
	if stack[0].questionCall == nil && stack[0].Text != "/compact" {
		for n < len(stack) && stack[n].questionCall == nil && stack[n].Text != "/compact" {
			n++
		}
	}
	return stack[:n], stack[n:]
}
