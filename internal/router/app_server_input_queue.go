package router

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Busy input follows Codex's composer queue: Enter steers the running turn,
// Tab queues input for the next turn, and interrupting with pending steers
// sends them at once as the next turn. Unlike Codex, waiting input stacks:
// unsent steers, and separately queued messages, go out as one message joined
// by newlines instead of one message per turn.
// Source: codex-rs/tui/src/chatwidget/{input_queue,input_flow,input_restore}.rs
// @86be5320b068ef67b56348b02aa8c33706955da6.

// composerSubmission is composer input sent to app-server as one message.
type composerSubmission struct {
	composerDraft
	parts     []composerDraft // The composer entries it joins, for input history.
	id        string          // clientUserMessageId, echoed by the committed userMessage.
	turn      string          // The steered turn; empty for turn/start.
	seq       uint64          // The transcript echo of a turn/start.
	committed bool            // The steer's userMessage arrived before its response.
	// The steered turn ended first; its input settles once this resolves.
	ended, interrupted, resend bool
}

// joinDrafts separates entries by newlines and renumbers their images.
func joinDrafts(parts ...composerDraft) composerDraft {
	var joined composerDraft
	for _, part := range parts {
		if part.text == "" {
			continue
		}
		if joined.text != "" {
			joined.text += "\n"
		}
		shift := len(joined.text)
		joined.text += part.text
		joined.attachments = append(joined.attachments, part.attachments...)
		if part.attachmentNotice != "" {
			joined.attachmentNotice = part.attachmentNotice
		}
		for _, image := range part.images {
			image.start, image.end = image.start+shift, image.end+shift
			joined.images = append(joined.images, image)
		}
		for _, skill := range part.skills {
			skill.start, skill.end = skill.start+shift, skill.end+shift
			joined.skills = append(joined.skills, skill)
		}
		for _, file := range part.files {
			file.start, file.end = file.start+shift, file.end+shift
			joined.files = append(joined.files, file)
		}
		joined.cursorBack = part.cursorBack // The caret stays in the last entry.
	}
	joined.renumberImages()
	return joined
}

func (u *appServerUI) steerParts(steers []composerSubmission) []composerDraft {
	var parts []composerDraft
	for _, steer := range steers {
		parts = append(parts, steer.parts...)
	}
	return parts
}

// takeDraft empties the composer. The caller stacks the draft, which keeps its
// image files until sent.
func (u *appServerUI) takeDraft() composerDraft {
	d := u.draftSnapshot()
	d.snapshotFileAttachments(u.session.cwd)
	d.cursorBack = 0
	u.loadDraft(composerDraft{})
	u.cursorColumn = nil
	u.historyBack, u.historyDraft = 0, composerDraft{}
	u.undoDrafts, u.redoDrafts = nil, nil
	u.run = runNone
	return d
}

// restoreDrafts returns input to the composer ahead of the current draft.
func (u *appServerUI) restoreDrafts(parts ...composerDraft) {
	parts = u.rejectQuestionParts(parts)
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
	u.undoDrafts, u.redoDrafts = nil, nil
	u.run = runNone
	u.cursorColumn = nil
	u.loadDraft(joinDrafts(append(slices.Clone(parts), u.draftSnapshot())...))
}

// editQueued returns the latest queued, else unsent, entry to the composer.
func (u *appServerUI) editQueued() {
	stack := &u.queued
	if len(*stack) == 0 {
		stack = &u.unsent
	}
	if len(*stack) == 0 {
		return
	}
	last := (*stack)[len(*stack)-1]
	*stack = (*stack)[:len(*stack)-1]
	u.restoreDrafts(last)
}

// flushInput sends stacked input once nothing blocks it: unsent steers into
// the running turn, otherwise unsent steers, then queued input, as a new turn.
func (u *appServerUI) flushInput() error {
	u.unsent = pendingQuestionReplies(u.unsent)
	u.queued = pendingQuestionReplies(u.queued)
	if u.shellOrigin != nil && u.turn != *u.shellOrigin {
		return nil // Main ended before Codex identified the shell's turn.
	}
	if u.waitingQuestion() || u.thread == "" || u.restoring != nil || u.submission.text != "" || u.starting || u.settingsPending || u.shellPending.text != "" || u.shellStandalone {
		return nil
	}
	parts, steer := u.unsent, u.turn != ""
	switch {
	case steer && u.turn == u.interrupting:
		return nil // The interrupted turn cannot take it; the next turn will.
	case len(u.unsent) > 0:
		parts, u.unsent = questionSubmissionBatch(u.unsent)
	case !steer && len(u.queued) > 0:
		parts, u.queued = questionSubmissionBatch(u.queued)
	default:
		return nil
	}
	return u.send(parts, steer)
}

func (u *appServerUI) send(parts []composerDraft, steer bool) error {
	s := composerSubmission{composerDraft: joinDrafts(parts...), parts: parts, id: rand.Text()}
	input := s.input()
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if len(s.attachments) > 0 && textBytes > composerTextLimit {
		u.restoreDrafts(parts...)
		u.setNotice("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.", true)
		return nil
	}
	params := map[string]any{"threadId": u.thread, "input": input, "clientUserMessageId": s.id}
	method := "turn/start"
	if steer {
		// The pending-input preview shows a steer until Codex commits it.
		method, s.turn = "turn/steer", u.turn
		params["expectedTurnId"] = u.turn
	} else {
		u.starting = true
		if len(questionReplies(s.text)) == 0 {
			s.seq = u.view.lastSeq + 1
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: s.seq, Agent: "You", Kind: "text", Text: s.text, Observed: time.Now(),
				native: &liveActivityNativeItem{thread: u.thread, item: fmt.Sprintf("input/%d", s.seq), phase: "input/pending", spans: s.composerDraft.displaySpans()}}}})
		}
	}
	for _, image := range s.images {
		delete(u.ownedImages, image.path)
	}
	u.submission, u.status, u.alert = s, "Sending…", false
	if s.attachmentNotice != "" {
		u.setNotice(s.attachmentNotice, true)
	}
	if err := u.request(method, params); err != nil {
		u.submission = composerSubmission{}
		u.withdraw(s)
		return err
	}
	for _, part := range parts {
		if part.questionCall != nil {
			part.questionCall.submissionID = s.id
		}
	}
	return nil
}

// withdraw returns a submission that app-server did not accept to the
// composer. Input stacked behind a failed start follows it back.
func (u *appServerUI) withdraw(s composerSubmission) {
	if s.turn != "" {
		u.restoreDrafts(s.parts...)
		return
	}
	u.starting = false
	u.view.removePendingInput(s.seq)
	u.restoreDrafts(slices.Concat(s.parts, u.unsent, u.queued)...)
	u.unsent, u.queued = nil, nil
}

func (u *appServerUI) submissionResponse(method string, failure *appserver.Error) {
	s := u.submission
	u.submission = composerSubmission{}
	if failure == nil {
		for i, part := range s.parts {
			if !part.inHistory {
				u.rememberInput(part)
				s.parts[i].inHistory = true
			}
		}
	}
	if failure != nil && len(s.parts) == 1 && s.parts[0].questionCall != nil {
		u.status, u.alert = method+": "+failure.Message, true
		u.withdraw(s)
		return
	}
	if s.ended {
		// Settle the ended turn with this steer in its place after earlier steers.
		if !s.committed {
			u.steers = append(u.steers, s)
		}
		u.settleSteers(s.interrupted, s.resend)
		return
	}
	if failure != nil {
		u.status, u.alert = method+": "+failure.Message, true
		u.withdraw(s)
		return
	}
	if u.status == "Sending…" && !u.alert {
		if u.turn != "" {
			u.status = "Working"
		} else if u.starting {
			u.status = "Starting turn…"
		}
	}
	switch {
	case s.turn == "" || s.committed:
		// turn/started may precede or follow a start's response. u.starting
		// prevents another start during that race.
	case s.turn == u.turn:
		u.steers = append(u.steers, s)
	default:
		u.restoreDrafts(s.parts...)
		u.setNotice("Turn ended before the steer · restored to the composer", false)
	}
}

// commitSteer matches Codex's committed user message to a pending steer by
// client ID, or by text from servers that do not echo one.
func (u *appServerUI) commitSteer(item appServerItem) {
	text, _, _ := appServerUserText(item.Content)
	matches := func(s composerSubmission) bool {
		if item.ClientID != "" {
			return item.ClientID == s.id
		}
		return text == s.text
	}
	if u.submission.turn != "" && matches(u.submission) {
		u.submission.committed = true
		return
	}
	if i := slices.IndexFunc(u.steers, matches); i >= 0 {
		u.steers = slices.Delete(u.steers, i, i+1)
	}
}

// settleInput resolves uncommitted steers when Main's turn ends, after any
// steer still being sent to that turn resolves.
func (u *appServerUI) settleInput(turn string, interrupted bool) {
	resend := interrupted && u.interrupting == turn && u.resendSteers
	u.interrupting, u.resendSteers = "", false
	if u.submission.turn == turn {
		u.submission.ended, u.submission.interrupted, u.submission.resend = true, interrupted, resend
		return
	}
	u.settleSteers(interrupted, resend)
}

// settleSteers: an interrupt discards uncommitted steers in Codex, so Ctrl-C's
// steers lead the next turn. Otherwise their delivery is unknown: they return
// to the composer and are never resent.
func (u *appServerUI) settleSteers(interrupted, resend bool) {
	steers := u.steerParts(u.steers)
	u.steers = nil
	switch {
	case resend:
		u.unsent = append(steers, u.unsent...)
		u.setNotice("Interrupted to send steer", false)
	case interrupted:
		// As in Codex, an interrupt returns queued input to the composer.
		u.restoreDrafts(slices.Concat(steers, u.queued)...)
		u.queued = nil
	case len(steers) > 0:
		u.restoreDrafts(steers...)
		u.setNotice("Unconfirmed steer restored to the composer", false)
	}
}

// pendingInputPreview lists waiting input above the composer, like Codex's
// pending input preview: at most three rows per entry.
// Source: codex-rs/tui/src/bottom_pane/pending_input_preview.rs@86be5320.
func (u *appServerUI) pendingInputPreview(width int) []string {
	var lines []string
	section := func(header string, parts []composerDraft) {
		if len(parts) == 0 {
			return
		}
		lines = append(lines, ansi.Truncate(activityui.Dim+"• "+activityui.Undim+header, width, "…")+activityui.Reset)
		for _, part := range parts {
			text := part.text
			if replies := questionReplies(text); len(replies) > 0 {
				text = fmt.Sprintf("Answering %d question(s)", len(replies))
			}
			rows := strings.Split(livediff.Safe(text, false), "\n")
			for i, row := range rows[:min(3, len(rows))] {
				prefix := "    "
				if i == 0 {
					prefix = "  ↳ "
				}
				lines = append(lines, ansi.Truncate(activityui.Dim+prefix+row, width, "…")+activityui.Reset)
			}
			if len(rows) > 3 {
				lines = append(lines, activityui.Dim+ansi.Truncate("    …", width, "")+activityui.Reset)
			}
		}
	}
	steers := u.steerParts(u.steers)
	if u.submission.turn != "" {
		steers = append(steers, u.submission.parts...)
	}
	header := "Steering after the next tool call · ctrl+c interrupts and sends now"
	switch {
	case u.shellStandalone:
		header = "Waiting for shell command to finish"
	case u.interrupting != "" || u.submission.resend:
		header = "Sending as the next turn once interrupted"
	case u.turn != "" || u.starting:
	case u.settingsPending:
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
	if stack[0].questionCall == nil {
		for n < len(stack) && stack[n].questionCall == nil {
			n++
		}
	}
	return stack[:n], stack[n:]
}
