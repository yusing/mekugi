package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

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

func (u *appServerUI) questionCalls() []*sessioncontrol.Question {
	calls := make([]*sessioncontrol.Question, 0, len(u.questions.Calls))
	for _, c := range u.questions.Calls {
		calls = append(calls, u.questions.handles[c])
	}
	return calls
}
func (u *appServerUI) activeQuestionCall() *sessioncontrol.Question {
	return u.questions.handles[u.questions.Active]
}
func (u *appServerUI) parkQuestion(q *prompt.Question) {
	state := u.Editor.Park()
	u.prompts.QuestionLayout(u.questions.View(u.Thread)).Column = u.mainView.CursorColumn
	if q.IsSecret {
		u.prompts.Secrets(u.questions.Active.ID).Editors[u.questions.Index] = state
	} else {
		q.Editor = state
	}
}
func (u *appServerUI) resumeQuestion(q *prompt.Question) {
	u.Editor = &u.conversation().Editor
	u.prompts.Secret = composer.Editor{}
	state := q.Editor
	if q.IsSecret {
		u.Editor = &u.prompts.Secret
		state = u.prompts.SecretState(u.questions.Active.ID).Editors[u.questions.Index]
	}
	u.Editor.Resume(state)
	u.mainView.CursorColumn = u.prompts.QuestionLayout(u.questions.View(u.Thread)).Column
}
func (u *appServerUI) discardQuestionEditors(c *sessioncontrol.Question) {
	u.prompts.ForgetCall(c.ID)
	for i := range c.Questions {
		c.Questions[i].Editor = composer.State{}
	}
}
func (u *appServerUI) currentQuestion() *prompt.Question { return u.questions.Current() }
func (u *appServerUI) questionCount() int                { return u.questions.Count(u.Thread) }
func (u *appServerUI) openQuestions() {
	if c := u.questions.Next(); c != nil {
		u.openQuestionCall(u.questions.handles[c])
	}
}

// Only an empty, idle composer yields to pending live questions. Explicitly
// hiding the dock and history restoration leave manual reopening available.
func (u *appServerUI) autoOpenQuestions() {
	if u.questions.ShouldOpen(u.composerVacant()) {
		u.openQuestions()
	}
}

// composerVacant reports an empty, idle composer with no overlay, which a
// pending dock may take over without capturing keys meant for a draft.
func (u *appServerUI) composerVacant() bool {
	if u.composerReadOnly() {
		return false
	}
	if u.Text != "" || len(u.Images) > 0 || len(u.Files) > 0 || len(u.Skills) > 0 || len(u.Selections) > 0 || u.pasted != nil || u.pickerVisible() || u.keybindings || u.statusPanel != nil || u.resumePicker != nil || u.approvals.Open {
		return false
	}
	return u.shell == nil || u.shell.Focus == 0 && !u.shell.input.Pasting() && !u.shell.input.Pending()
}
func (u *appServerUI) openQuestionCall(c *sessioncontrol.Question) {
	if u.questions.Active != nil {
		return
	}
	u.hideApprovals()
	u.prompts.Reopen()
	u.mainView.PromptColumn = u.mainView.CursorColumn
	u.promptEditor.Open(u.Editor, composer.State{})
	if !u.questions.Open(c.Call) {
		return
	}
	u.resumeQuestion(u.currentQuestion())
	u.focusPrompt()
}
func (u *appServerUI) focusPrompt() {
	u.picker.Open, u.keybindings = false, false
	u.cancelPickerScan()
	if u.shell != nil {
		u.shell.Focus = 0
		u.shell.selection = nil
	}
}
func (u *appServerUI) hideQuestions() {
	if q := u.currentQuestion(); q != nil {
		u.parkQuestion(q)
		u.Editor = &u.conversation().Editor
		u.prompts.Secret = composer.Editor{}
		u.promptEditor.Close(u.Editor)
		u.mainView.CursorColumn = u.mainView.PromptColumn
		u.questions.Hide()
	}
}
func (u *appServerUI) moveQuestion(step int) {
	u.parkQuestion(u.currentQuestion())
	u.questions.Move(step)
	u.resumeQuestion(u.currentQuestion())
}
func (u *appServerUI) addQuestions(c *sessioncontrol.Question, replay bool) {
	if c.Thread != u.Thread && !c.Local || !u.questions.Add(c.Call, replay) {
		return
	}
	u.bindQuestion(c, replay)
}

// bindQuestion attaches live handles and terminal effects after neutral admission.
func (u *appServerUI) bindQuestion(c *sessioncontrol.Question, replay bool) {
	if u.questions.handles == nil {
		u.questions.handles = make(map[*prompt.Call]*sessioncontrol.Question)
	}
	u.questions.handles[c.Call] = c
	if !replay && !c.Local {
		if c.Sync {
			u.notify("plan-mode-prompt", "Input requested")
		} else {
			u.notify("async-question", "Question pending")
		}
	}
	u.renderQuestionRecord(c)
	if !replay {
		u.autoOpenQuestions()
	}
}

func (u *appServerUI) questionMessage(m appserver.Message) (bool, error) {
	if m.Method == "item/tool/requestUserInput" && len(m.ID) > 0 {
		p, err := binding.DecodeQuestionRequest(m.Params)
		if err != nil {
			return true, err
		}
		if p.ThreadID != u.Thread {
			return false, nil
		}
		call := conversationbinding.Questions(p.ThreadID, p.TurnID, p.ItemID, m.ID, p.Questions)
		u.addQuestions(&sessioncontrol.Question{Call: &call, RequestID: slices.Clone(m.ID)}, false)
		return true, nil
	}
	if m.Method == "serverRequest/resolved" {
		p, err := binding.DecodeResolvedRequest(m.Params)
		if err != nil {
			return true, err
		}
		for _, c := range u.questionCalls() {
			if c.Thread == p.ThreadID && string(c.RequestID) == string(p.RequestID) {
				u.resolveQuestionCall(c, "answered elsewhere")
			}
		}
		return true, nil
	}
	return false, nil
}

func (u *appServerUI) saveUnsentAnswer(d composer.Draft) {
	if d.Text != "" {
		u.rememberInput(conversationmodel.Input{Draft: d})
		u.setNotice("unsent answer saved to input history", false)
	}
}
func (u *appServerUI) resolveQuestionCall(c *sessioncontrol.Question, outcome string) {
	if c.Resolved {
		return
	}
	if u.activeQuestionCall() == c {
		u.hideQuestions()
	}
	for _, d := range c.Resolve(outcome) {
		u.saveUnsentAnswer(d)
	}
	u.discardQuestionEditors(c)
	if c.Sudo != nil {
		if c.CancelPassword() {
			u.recordApproval(&sessioncontrol.Approval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.Sudo.Item}}, "Denied: sudo authentication canceled")
		}
		u.questions.Remove(c.Call)
		delete(u.questions.handles, c.Call)
		return
	}
	u.renderQuestionRecord(c)
	if c.Sync && c.Sent {
		u.recordSyncQuestionAnswer(c)
	}
}

// Synchronous answers have no user-message envelope. Retain their committed
// response as the same hidden chronological anchor used by async answers.
func (u *appServerUI) recordSyncQuestionAnswer(c *sessioncontrol.Question) {
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
	for _, v := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
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
		v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question_reply", Text: strings.Join(answers, "\n"), Observed: u.now(), Native: native}}})
	}
}
func (u *appServerUI) endSyncQuestions(turn string) {
	for _, c := range slices.Clone(u.questionCalls()) {
		if c.Turn == turn && (len(c.RequestID) > 0 || c.Sudo != nil) {
			u.resolveQuestionCall(c, "interrupted")
		}
	}
}

func (u *appServerUI) questionKey(key string) (bool, error) {
	q := u.currentQuestion()
	if q == nil {
		return false, nil
	}
	c := u.activeQuestionCall()
	view := u.sessionPresentation().View()
	keyInput := u.prompts.QuestionLayout(view.Questions).Key(view.Questions, key, u.Text == "")
	if err := u.sessionPresentation().QuestionInput(sessionpresentation.QuestionInputIntent{Version: view.Version, QuestionInput: keyInput.Question}, u.questions.Questions); err != nil {
		u.setNotice(err.Error(), true)
		return true, nil
	}
	action := keyInput.Action
	switch action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		u.questions.AutoOpen = false
		u.hideQuestions()
	case conversationui.PromptCursor:
		u.moveDraft(key)
	case conversationui.PromptPrevious:
		u.moveQuestion(-1)
	case conversationui.PromptNext:
		u.moveQuestion(1)
	case conversationui.PromptAttach:
		u.pasteImage()
	case conversationui.PromptSubmit:
		return true, u.submitQuestions(sessionpresentation.QuestionSubmitIntent{Version: u.sessionPresentation().View().Version, Call: c.ID, Incomplete: true})
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		u.parkQuestion(q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete(u.Text, c.Sync, action == conversationui.PromptSkip) {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			u.prompts.Secrets(c.ID).Answers[u.questions.Index] = answer.Answer
		} else {
			view := u.sessionPresentation().View()
			if !u.answerQuestion(sessionpresentation.AnswerIntent{Version: view.Version, AnswerInput: prompt.AnswerInput{
				Call: c.ID, Question: view.Questions.Call.Questions[u.questions.Index].ID,
				Choice: q.Selected, Draft: q.Editor.Snapshot.Draft, Skip: action == conversationui.PromptSkip,
			}}) {
				return true, nil
			}
		}
		if step, submit := u.questions.Advance(); step != 0 {
			u.moveQuestion(step)
		} else if submit {
			return true, u.submitQuestions(sessionpresentation.QuestionSubmitIntent{Version: u.sessionPresentation().View().Version, Call: c.ID})
		}
	}
	return true, nil
}
func (u *appServerUI) answerQuestion(intent sessionpresentation.AnswerIntent) bool {
	defer u.settlePresentation()
	q, err := u.sessionPresentation().AnswerQuestion(intent, u.questions.Questions)
	if err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	if q == u.currentQuestion() {
		u.resumeQuestion(q)
	}
	return true
}

func (u *appServerUI) submitQuestions(intent sessionpresentation.QuestionSubmitIntent) error {
	defer u.settlePresentation()
	record, err := u.sessionPresentation().SubmitQuestions(intent, u.questions.Questions)
	if err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	c := u.questions.handles[record]
	defer func() {
		u.pruneDraftImages()
		u.questions.AutoOpen = true
		u.autoOpenQuestions()
	}()
	if c.Sudo != nil {
		return u.submitSudoPassword(c)
	}
	attached := questionAnswerAttachments(c.Call, u.prompts.SecretState(c.ID).Editors, u.prompts.SecretState(c.ID).Answers)
	if len(c.RequestID) > 0 {
		if err := c.Answer(u.requests, u.prompts.SecretState(c.ID).Answers); err != nil {
			return err
		}
		if u.questions.Active == c.Call {
			u.hideQuestions()
		}
		c.Submitted()
		clear(u.prompts.SecretState(c.ID).Editors)
		clear(u.prompts.SecretState(c.ID).Answers)
		u.renderQuestionRecord(c)
		if len(attached.AnswerImages) > 0 || len(attached.Attachments) > 0 {
			d := conversationmodel.Input{Draft: attached}
			d.Text, d.Question, d.InHistory = "Attachments accompanying answers to "+c.Item+":\n", c.Call, true
			d.AnswerImages = nil
			for i, path := range attached.AnswerImages {
				label := fmt.Sprintf("[Image %d]", i+1)
				start := len(d.Text)
				d.Text += label + "\n"
				d.Images = append(d.Images, composer.Token{Start: start, End: start + len(label), Path: path})
			}
			u.conversation().Unsent = append(u.conversation().Unsent, d)
			return u.flushInput()
		}
		return nil
	}
	reply := conversationbinding.QuestionReplies(c.Call)
	if u.questions.Active == c.Call {
		u.hideQuestions()
	}
	c.Submitted()
	if reply == "" {
		c.Resolved = true
		u.renderQuestionRecord(c)
		return nil
	}
	attached.Text, attached.InHistory = reply, true
	u.conversation().Unsent = append(u.conversation().Unsent, conversationmodel.Input{Draft: attached, Question: c.Call, ReplyCount: len(binding.QuestionReplies(reply))})
	u.renderQuestionRecord(c)
	return u.flushInput()
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
func (u *appServerUI) renderQuestionRecord(c *sessioncontrol.Question) {
	if c.Sudo != nil {
		return
	}
	states := []string{}
	var body []string
	var records []activitypresentation.Question
	for _, q := range c.View().Questions {
		state := q.State
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
		record := activitypresentation.Question{Text: q.Text, State: state}
		line := q.Text
		if len(q.Answer) > 0 {
			answers := q.Answer
			record.Answer = strings.Join(answers, "\n\n")
			line += "\n└ " + record.Answer
		} else if q.Pending && len(q.Choices) > 0 {
			labels := []string{}
			for _, o := range q.Choices {
				labels = append(labels, o.Label)
			}
			record.Options = strings.Join(labels, " · ")
			line += "\n" + record.Options
		}
		body = append(body, line)
		records = append(records, record)
	}
	label := fmt.Sprintf("%d questions · %s", len(c.Questions), strings.Join(states, ", "))
	if len(c.Questions) == 1 {
		label = strings.Replace(label, "questions", "question", 1)
	}
	if len(c.RequestID) == 0 && !c.Resolved && u.Turn == "" {
		label += " · answer starts a new turn"
	}
	text := label + "\n" + strings.Join(body, "\n\n")
	id := c.Item
	if len(c.RequestID) > 0 {
		id = "question/" + string(c.RequestID)
	}
	for _, v := range []*sessionpresentation.ActivitySource{u.source, u.childSource} {
		if v == nil {
			continue
		}
		entry := sessionpresentation.ActivityEntry{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question", Text: text, Observed: u.now(), Native: &sessionpresentation.NativeItem{Thread: c.Thread, Turn: c.Turn, Item: id, Phase: "question", Questions: records}}
		v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
	}
}

// mainTitleDetail follows Main's pane name: unanswered questions, then the
// skills loaded into Main's current context.
func (u *appServerUI) mainTitleDetail() string {
	details := []string{u.questionBadge()}
	if set := u.source.ActiveSkills()["Main"]; set != nil {
		details = append(details, activityui.SkillLabel(set))
	}
	return strings.Join(slices.DeleteFunc(details, func(detail string) bool { return detail == "" }), terminalui.Dim+" · "+terminalui.Undim)
}

func (u *appServerUI) questionBadge() string {
	if u.shell != nil && u.shell.Focus != 0 {
		if n := u.questionCount(); n > 0 {
			return fmt.Sprintf("?%d", n)
		}
	}
	return ""
}
