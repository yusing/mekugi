package router

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/contracts/scope"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"

	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

type nativeQuestionDock struct {
	*prompt.Questions
	handles map[*prompt.Call]*sessioncontrol.Question
}

func (q *nativeQuestionDock) calls() []*sessioncontrol.Question {
	calls := make([]*sessioncontrol.Question, 0, len(q.Calls))
	for _, c := range q.Calls {
		calls = append(calls, q.handles[c])
	}
	return calls
}
func (q *nativeQuestionDock) activeCall() *sessioncontrol.Question {
	return q.handles[q.Active]
}
func parkQuestion(questions *nativeQuestionDock, terminal *terminalsession.Attachment, editor *composer.Editor, main string, q *prompt.Question) {
	state := editor.Park()
	terminal.Prompts.QuestionLayout(questions.View(main)).Column = terminal.Main.CursorColumn
	if q.IsSecret {
		terminal.Prompts.Secrets(questions.Active.ID).Editors[questions.Index] = state
	} else {
		q.Editor = state
	}
}
func resumeQuestion(questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, main string, q *prompt.Question) *composer.Editor {
	editor := &conversation.Editor
	terminal.Prompts.Secret = composer.Editor{}
	state := q.Editor
	if q.IsSecret {
		editor = &terminal.Prompts.Secret
		state = terminal.Prompts.SecretState(questions.Active.ID).Editors[questions.Index]
	}
	editor.Resume(state)
	terminal.Main.CursorColumn = terminal.Prompts.QuestionLayout(questions.View(main)).Column
	return editor
}
func discardQuestionEditors(prompts *terminalsession.Prompts, c *sessioncontrol.Question) {
	prompts.ForgetCall(c.ID)
	for i := range c.Questions {
		c.Questions[i].Editor = composer.State{}
	}
}

func openQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, main string) {
	if c := questions.Next(); c != nil {
		openQuestionCall(questions, approvals, attached.conversation, terminal, picker, editor, main, questions.handles[c])
	}
}

// Only an empty, idle composer yields to pending live questions. Explicitly
// hiding the dock and history restoration leave manual reopening available.
func autoOpenQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, main string, overlay bool) {
	if questions.ShouldOpen(composerVacant((*editor), terminal, picker, attached.presentation, overlay || approvals.Open)) {
		openQuestions(attached, terminal, picker, editor, questions, approvals, main)
	}
}

// composerVacant reports an empty, idle composer with no overlay, which a
// pending dock may take over without capturing keys meant for a draft.
func composerVacant(editor *composer.Editor, terminal *terminalsession.Attachment, picker *composerPicker, presentation *sessionpresentation.Model, overlay bool) bool {
	if presentation.View().ReadOnly != sessionpresentation.Editable {
		return false
	}
	if editor.Text != "" || len(editor.Images) > 0 || len(editor.Files) > 0 || len(editor.Skills) > 0 || len(editor.Selections) > 0 || terminal.Main.Pasting() || pickerVisible(picker, presentation, terminal) || terminal.Main.Keybindings || overlay {
		return false
	}
	return terminal == nil || terminal.Focus == 0 && !terminal.Decoder.Pasting() && !terminal.Decoder.Pending()
}
func openQuestionCall(questions *nativeQuestionDock, approvals *nativeApprovalDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, main string, c *sessioncontrol.Question) {
	if questions.Active != nil {
		return
	}
	hideApprovals(approvals, terminal, &conversation.PromptEditor, *editor)
	terminal.Prompts.Reopen()
	terminal.Main.PromptColumn = terminal.Main.CursorColumn
	conversation.PromptEditor.Open(*editor, composer.State{})
	if !questions.Open(c.Call) {
		return
	}
	*editor = resumeQuestion(questions, conversation, terminal, main, questions.Current())
	focusPrompt(picker, terminal)
}
func focusPrompt(picker *composerPicker, terminal *terminalsession.Attachment) {
	picker.Open, terminal.Main.Keybindings = false, false
	picker.cancelScan()
	if terminal != nil {
		terminal.Focus = 0
		terminal.Selection = nil
	}
}
func hideQuestionEditor(questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, editor *composer.Editor, main string) *composer.Editor {
	if q := questions.Current(); q != nil {
		parkQuestion(questions, terminal, editor, main, q)
		editor = &conversation.Editor
		terminal.Prompts.Secret = composer.Editor{}
		conversation.PromptEditor.Close(editor)
		terminal.Main.CursorColumn = terminal.Main.PromptColumn
		questions.Hide()
	}
	return editor
}

// carryLocal preserves unresolved local authentication without recording a host question.
func (q *nativeQuestionDock) carryLocal(conversation *conversationmodel.Session, previous []*sessioncontrol.Question, main string) {
	*q = nativeQuestionDock{Questions: &conversation.Questions}
	for _, c := range previous {
		if c.Sudo == nil || c.Resolved || c.Thread != main && !c.Local || !q.Add(c.Call, true) {
			continue
		}
		if q.handles == nil {
			q.handles = make(map[*prompt.Call]*sessioncontrol.Question)
		}
		q.handles[c.Call] = c
	}
}

func moveQuestion(questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, editor **composer.Editor, main string, step int) {
	parkQuestion(questions, terminal, *editor, main, questions.Current())
	questions.Move(step)
	*editor = resumeQuestion(questions, conversation, terminal, main, questions.Current())
}
func addQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, c *sessioncontrol.Question, replay bool) {
	if c.Thread != control.Thread && !c.Local || !questions.Add(c.Call, replay) {
		return
	}
	bindQuestion(attached, terminal, control, picker, editor, questions, approvals, clock, overlay, notifications, c, replay)
}

// bindQuestion attaches live handles and terminal effects after neutral admission.
func bindQuestion(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, c *sessioncontrol.Question, replay bool) {
	if questions.handles == nil {
		questions.handles = make(map[*prompt.Call]*sessioncontrol.Question)
	}
	questions.handles[c.Call] = c
	if !replay && !c.Local {
		if c.Sync {
			notifications.notify(&attached.feedback, terminal, clock, "plan-mode-prompt", "Input requested")
		} else {
			notifications.notify(&attached.feedback, terminal, clock, "async-question", "Question pending")
		}
	}
	renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
	if !replay {
		autoOpenQuestions(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
	}
}

func questionMessage(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, m appserver.Message) (bool, error) {
	if m.Method == "item/tool/requestUserInput" && len(m.ID) > 0 {
		p, err := binding.DecodeQuestionRequest(m.Params)
		if err != nil {
			return true, err
		}
		if p.ThreadID != control.Thread {
			return false, nil
		}
		call := conversationbinding.Questions(p.ThreadID, p.TurnID, p.ItemID, m.ID, p.Questions)
		addQuestions(attached, terminal, control, picker, editor, questions, approvals, clock, overlay, notifications, &sessioncontrol.Question{Call: &call, RequestID: slices.Clone(m.ID)}, false)
		return true, nil
	}
	if m.Method == "serverRequest/resolved" {
		p, err := binding.DecodeResolvedRequest(m.Params)
		if err != nil {
			return true, err
		}
		for _, c := range questions.calls() {
			if c.Thread == p.ThreadID && string(c.RequestID) == string(p.RequestID) {
				resolveQuestionCall(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "answered elsewhere")
			}
		}
		return true, nil
	}
	return false, nil
}

func saveUnsentAnswer(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, d composer.Draft) {
	if d.Text != "" {
		rememberInput(attached.conversation, conversationmodel.Input{Draft: d})
		setNotice(&attached.feedback, terminal, presentationTime(clock), "unsent answer saved to input history", false)
	}
}
func resolveQuestionCall(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, c *sessioncontrol.Question, outcome string) {
	if c.Resolved {
		return
	}
	if questions.activeCall() == c {
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	}
	for _, d := range c.Resolve(outcome) {
		saveUnsentAnswer(attached, terminal, clock, d)
	}
	discardQuestionEditors(terminal.Prompts, c)
	if c.Sudo != nil {
		if c.CancelPassword() {
			recordApproval(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, "Denied: sudo authentication canceled")
		}
		questions.Remove(c.Call)
		delete(questions.handles, c.Call)
		return
	}
	renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
	if c.Sync && c.Sent {
		recordSyncQuestionAnswer(attached, presentationTime(clock), c)
	}
}

// Synchronous answers have no user-message envelope. Retain their committed
// response as the same hidden chronological anchor used by async answers.
func recordSyncQuestionAnswer(attached *attachedPresentation, at time.Time, c *sessioncontrol.Question) {
	var answers []string
	for _, q := range c.Questions {
		if q.Outcome != "answered" {
			continue
		}
		if q.IsSecret {
			answers = append(answers, "•••")
			continue
		}
		answers = append(answers, q.Answer...)
	}
	if len(answers) == 0 {
		return
	}
	for _, v := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		if v == nil {
			continue
		}
		native := &sessionpresentation.NativeItem{Thread: c.Thread, Turn: c.Turn, Item: "question-answer/" + string(c.RequestID), Phase: "item/completed"}
		for _, previous := range slices.Backward(v.Entries) {
			if previous.Source.Native == nil || previous.Source.Native.Thread != c.Thread {
				continue
			}
			if previous.Source.Kind == "question" && previous.Source.Native.Item == "question/"+string(c.RequestID) {
				native.Question = previous.Source.Seq
			}
			if native.ReplySource == "" && previous.Source.Agent == "You" && previous.Source.Native.Turn == c.Turn {
				native.ReplySource = previous.Source.Text
			}
		}
		v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question_reply", Text: strings.Join(answers, "\n"), Observed: at, Native: native}}})
	}
}
func endSyncQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, turn string) {
	for _, c := range slices.Clone(questions.calls()) {
		if c.Turn == turn && (len(c.RequestID) > 0 || c.Sudo != nil) {
			resolveQuestionCall(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, c, "interrupted")
		}
	}
}

func applyQuestionInput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, environment []string, owned *map[string]bool, hook *approvalHookCheck, parent context.Context, observations *sessioncontrol.Observations, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := attached.presentation.QuestionInput(sessionpresentation.QuestionInputIntent{Version: input.Version, QuestionInput: keyInput.Question}, questions.Questions); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return true, nil
	}
	q, c := questions.Current(), questions.activeCall()
	action := keyInput.Action
	switch action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		questions.AutoOpen = false
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	case conversationui.PromptCursor:
		return false, nil
	case conversationui.PromptPrevious:
		moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, -1)
	case conversationui.PromptNext:
		moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, 1)
	case conversationui.PromptAttach:
		view := publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, presentationTime(clock))
		intent := sessionpresentation.ComposerIntent{Version: view.Version, Target: view.EditorTarget(), Operation: sessionpresentation.AttachClipboard}
		if err := attached.presentation.AdmitComposer(intent, attached.conversation); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return true, nil
		}
		pasteImage(attached, terminal, editor, questions, approvals, clock, owned, side, parent, intent)
		return true, nil
	case conversationui.PromptSubmit:
		return true, submitQuestions(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, environment, (*owned), hook, parent, observations, reviewOwner, receipts, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID, Incomplete: true})
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		parkQuestion(questions, terminal, (*editor), control.Thread, q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete((*editor).Text, c.Sync, action == conversationui.PromptSkip) {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			terminal.Prompts.Secrets(c.ID).Answers[questions.Index] = answer.Answer
		} else {
			view := attached.presentation.View()
			if !answerQuestion(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, sessionpresentation.AnswerIntent{Version: view.Version, AnswerInput: prompt.AnswerInput{
				Call: c.ID, Question: view.Questions.Call.Questions[questions.Index].ID,
				Choice: q.Selected, Draft: q.Editor.Snapshot.Draft, Skip: action == conversationui.PromptSkip,
			}}) {
				return true, nil
			}
		}
		if step, submit := questions.Advance(); step != 0 {
			moveQuestion(questions, attached.conversation, terminal, editor, control.Thread, step)
		} else if submit {
			return true, submitQuestions(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, environment, (*owned), hook, parent, observations, reviewOwner, receipts, sessionpresentation.QuestionSubmitIntent{Version: attached.presentation.View().Version, Call: c.ID})
		}
	}
	return true, nil
}
func answerQuestion(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, intent sessionpresentation.AnswerIntent) bool {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	q, err := attached.presentation.AnswerQuestion(intent, questions.Questions)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	if q == questions.Current() {
		(*editor) = resumeQuestion(questions, attached.conversation, terminal, control.Thread, q)
	}
	return true
}

func submitQuestions(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, environment []string, owned map[string]bool, hook *approvalHookCheck, parent context.Context, observations *sessioncontrol.Observations, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessionpresentation.QuestionSubmitIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	record, err := attached.presentation.SubmitQuestions(intent, questions.Questions)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	c := questions.handles[record]
	defer func() {
		pruneDraftImages(owned, (*editor), attached.conversation, questions, approvals, terminal.Prompts, side)
		questions.AutoOpen = true
		autoOpenQuestions(attached, terminal, picker, editor, questions, approvals, control.Thread, overlay)
	}()
	if c.Sudo != nil {
		return submitSudoPassword(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, owned, hook, editor, parent, restoring, observations, reviewOwner, receipts, c)
	}
	answerDraft := questionAnswerAttachments(c.Call, terminal.Prompts.SecretState(c.ID).Editors, terminal.Prompts.SecretState(c.ID).Answers)
	if len(c.RequestID) > 0 {
		if err := c.Answer(control.Requests, terminal.Prompts.SecretState(c.ID).Answers); err != nil {
			return err
		}
		if questions.Active == c.Call {
			(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
		}
		c.Submitted()
		clear(terminal.Prompts.SecretState(c.ID).Editors)
		clear(terminal.Prompts.SecretState(c.ID).Answers)
		renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
		if len(answerDraft.AnswerImages) > 0 || len(answerDraft.Attachments) > 0 {
			d := conversationmodel.Input{Draft: answerDraft}
			d.Text, d.Question, d.InHistory = "Attachments accompanying answers to "+c.Item+":\n", c.Call, true
			d.AnswerImages = nil
			for i, path := range answerDraft.AnswerImages {
				label := fmt.Sprintf("[Image %d]", i+1)
				start := len(d.Text)
				d.Text += label + "\n"
				d.Images = append(d.Images, composer.Token{Start: start, End: start + len(label), Path: path})
			}
			attached.conversation.Unsent = append(attached.conversation.Unsent, d)
			return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, owned, hook, editor, parent, restoring, observations, reviewOwner, receipts)
		}
		return nil
	}
	reply := conversationbinding.QuestionReplies(c.Call)
	if questions.Active == c.Call {
		(*editor) = hideQuestionEditor(questions, attached.conversation, terminal, (*editor), control.Thread)
	}
	c.Submitted()
	if reply == "" {
		c.Resolved = true
		renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
		return nil
	}
	answerDraft.Text, answerDraft.InHistory = reply, true
	attached.conversation.Unsent = append(attached.conversation.Unsent, conversationmodel.Input{Draft: answerDraft, Question: c.Call, ReplyCount: len(binding.QuestionReplies(reply))})
	renderQuestionRecord(attached, control.Turn, presentationTime(clock), c)
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, owned, hook, editor, parent, restoring, observations, reviewOwner, receipts)
}

// Answers use composer attachment snapshots. Keep the async reply envelope
// intact while the host processes accompanying images and quoted selections.
func questionAnswerAttachments(c *prompt.Call, secrets map[int]composer.State, secretAnswers map[int][]string) composer.Draft {
	var paths []string
	var drafts []composer.Draft
	for i := range c.Questions {
		q := &c.Questions[i]
		if !q.Done || q.Skipped || q.Outcome != "" {
			continue
		}
		d := q.Editor.Snapshot.Draft
		if q.IsSecret {
			d = secrets[i].Snapshot.Draft
		}
		if len(d.Images) == 0 && !c.Sync && q.Selected < len(q.Choices) {
			continue // Async option-only replies do not submit the parked free text.
		}
		drafts = append(drafts, d)
		if len(d.Images) == 0 {
			continue
		}
		text := d.Text
		for j := len(d.Images) - 1; j >= 0; j-- {
			image := d.Images[j]
			text = text[:image.Start] + fmt.Sprintf("[Image %d]", len(paths)+j+1) + text[image.End:]
		}
		for _, image := range d.Images {
			paths = append(paths, image.Path)
		}
		answer := []string{text}
		if q.Selected < len(q.Choices) {
			answer = []string{q.Choices[q.Selected].Label, text}
		}
		if q.IsSecret {
			secretAnswers[i] = answer
		} else {
			q.Answer = answer
		}
	}
	attached := composer.Join(drafts...)
	attached.SnapshotFiles("") // Answer editors bind selections, not files.
	return composer.Draft{AnswerImages: paths, Attachments: attached.Attachments, AttachmentNotice: attached.AttachmentNotice}
}

// Already-transmitted input belongs to Codex. Only local unsent batches can
// be rebuilt after another client answers some or all of their questions.
func pendingQuestionReplies(parts []conversationmodel.Input) []conversationmodel.Input {
	var pending []conversationmodel.Input
	for _, part := range parts {
		c := part.Question
		if c == nil {
			pending = append(pending, part)
			continue
		}
		if c.Sync {
			pending = append(pending, part) // Sync attachments follow the completed tool response.
			continue
		}
		if c.Resolved {
			continue
		}
		attached := questionAnswerAttachments(c, nil, nil)
		part.AnswerImages, part.Attachments, part.AttachmentNotice = attached.AnswerImages, attached.Attachments, attached.AttachmentNotice
		part.Text = conversationbinding.PendingQuestionReply(c, part.Text)
		part.ReplyCount = len(binding.QuestionReplies(part.Text))
		if part.Text == "" {
			continue
		}
		pending = append(pending, part)
	}
	return pending
}
func renderQuestionRecord(attached *attachedPresentation, turn string, at time.Time, c *sessioncontrol.Question) {
	if c.Sudo == nil {
		sessionpresentation.RecordQuestion(c.Call, string(c.RequestID), turn, at, attached.source, attached.childSource)
	}
}
