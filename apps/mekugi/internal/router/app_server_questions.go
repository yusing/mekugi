package router

import (
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

type nativeQuestionCall struct {
	*prompt.Call
	request jsontext.Value
	sudo    *vcsApproval
	// Secret state remains invocation-local and is never part of a neutral view.
	secrets       map[int]questionEditor
	secretAnswers map[int][]string
}

type questionEditor struct {
	composer.State
	column *int
}
type nativeQuestionDock struct {
	*prompt.Questions
	handles map[*prompt.Call]*nativeQuestionCall
	layout  map[*prompt.Question]*conversationui.QuestionLayout
	rect    terminalsession.Rect
	painted bool
}

func (u *appServerUI) questionCalls() []*nativeQuestionCall {
	calls := make([]*nativeQuestionCall, 0, len(u.questions.Calls))
	for _, c := range u.questions.Calls {
		calls = append(calls, u.questions.handles[c])
	}
	return calls
}
func (u *appServerUI) activeQuestionCall() *nativeQuestionCall {
	return u.questions.handles[u.questions.Active]
}
func (u *appServerUI) questionLayout(q *prompt.Question) *conversationui.QuestionLayout {
	if q == nil {
		return nil
	}
	if u.questions.layout == nil {
		u.questions.layout = make(map[*prompt.Question]*conversationui.QuestionLayout)
	}
	if u.questions.layout[q] == nil {
		u.questions.layout[q] = &conversationui.QuestionLayout{}
	}
	return u.questions.layout[q]
}
func (u *appServerUI) parkQuestion(q *prompt.Question) {
	e := u.saveQuestionEditor()
	if q.IsSecret {
		c := u.activeQuestionCall()
		if c.secrets == nil {
			c.secrets = make(map[int]questionEditor)
		}
		c.secrets[u.questions.Index] = e
	} else {
		q.Editor = e.State
		u.questionLayout(q).Column = e.column
	}
}
func (u *appServerUI) resumeQuestion(q *prompt.Question) {
	u.Editor = &u.conversation().Editor
	u.secretEditor = composer.Editor{}
	if q.IsSecret {
		u.Editor = &u.secretEditor
		u.loadQuestionEditor(u.activeQuestionCall().secrets[u.questions.Index])
		return
	}
	u.loadQuestionEditor(questionEditor{q.Editor, u.questionLayout(q).Column})
}
func (u *appServerUI) discardQuestionEditors(c *nativeQuestionCall) {
	clear(c.secrets)
	clear(c.secretAnswers)
	for i := range c.Questions {
		c.Questions[i].Editor = composer.State{}
		delete(u.questions.layout, &c.Questions[i])
	}
}
func (u *appServerUI) saveQuestionEditor() questionEditor {
	return questionEditor{u.Editor.Park(), u.cursorColumn}
}
func (u *appServerUI) loadQuestionEditor(e questionEditor) {
	u.Editor.Resume(e.State)
	u.cursorColumn = e.column
}
func (u *appServerUI) currentQuestion() *prompt.Question { return u.questions.Current() }
func (u *appServerUI) questionCount() int                { return u.questions.Count(u.Thread) }
func (u *appServerUI) waitingQuestion() bool             { return u.questions.Waiting(u.Thread) }
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
func (u *appServerUI) openQuestionCall(c *nativeQuestionCall) {
	if u.questions.Active != nil {
		return
	}
	u.hideApprovals()
	u.questions.painted = false
	u.promptColumn = u.cursorColumn
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
		u.secretEditor = composer.Editor{}
		u.promptEditor.Close(u.Editor)
		u.cursorColumn = u.promptColumn
		u.questions.Hide()
	}
}
func (u *appServerUI) moveQuestion(step int) {
	u.parkQuestion(u.currentQuestion())
	u.questions.Move(step)
	u.resumeQuestion(u.currentQuestion())
}
func (u *appServerUI) addQuestions(c *nativeQuestionCall, replay bool) {
	if c.Thread != u.Thread && !c.Local || !u.questions.Add(c.Call, replay) {
		return
	}
	u.bindQuestion(c, replay)
}

// bindQuestion attaches live handles and terminal effects after neutral admission.
func (u *appServerUI) bindQuestion(c *nativeQuestionCall, replay bool) {
	if u.questions.handles == nil {
		u.questions.handles = make(map[*prompt.Call]*nativeQuestionCall)
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
		u.addQuestions(&nativeQuestionCall{Call: &call, request: slices.Clone(m.ID)}, false)
		return true, nil
	}
	if m.Method == "serverRequest/resolved" {
		p, err := binding.DecodeResolvedRequest(m.Params)
		if err != nil {
			return true, err
		}
		for _, c := range u.questionCalls() {
			if c.Thread == p.ThreadID && string(c.request) == string(p.RequestID) {
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
func (u *appServerUI) resolveQuestionCall(c *nativeQuestionCall, outcome string) {
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
	if c.sudo != nil {
		if !c.Sent && !c.sudo.finished() {
			c.sudo.reply <- shellapproval.Reply{Reason: "sudo authentication canceled"}
			u.recordApproval(&nativeApproval{Approval: &prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.sudo.item}}, "Denied: sudo authentication canceled")
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
func (u *appServerUI) recordSyncQuestionAnswer(c *nativeQuestionCall) {
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
	for _, v := range []*activityAdapter{u.view, u.agents} {
		if v == nil {
			continue
		}
		native := &liveActivityNativeItem{thread: c.Thread, turn: c.Turn, item: "question-answer/" + string(c.request), phase: "item/completed"}
		for _, previous := range slices.Backward(v.Entries) {
			if previous.Source.native == nil || previous.Source.native.thread != c.Thread {
				continue
			}
			if previous.Source.Kind == "question" && previous.Source.native.item == "question/"+string(c.request) {
				native.question = previous.Source.Seq
			}
			if native.replySource == "" && previous.Source.Agent == "You" && previous.Source.native.turn == c.Turn {
				native.replySource = previous.Source.Text
			}
		}
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question_reply", Text: strings.Join(answers, "\n"), Observed: u.now(), native: native}}})
	}
}
func (u *appServerUI) endSyncQuestions(turn string) {
	for _, c := range slices.Clone(u.questionCalls()) {
		if c.Turn == turn && (len(c.request) > 0 || c.sudo != nil) {
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
	action := u.questionLayout(q).Key(u.questions.Questions, key, u.Text == "")
	switch action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
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
		return true, u.submitQuestions()
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		u.parkQuestion(q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete(u.Text, c.Sync, action == conversationui.PromptSkip) {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			if c.secretAnswers == nil {
				c.secretAnswers = make(map[int][]string)
			}
			c.secretAnswers[u.questions.Index] = answer.Answer
		} else if !q.Complete(u.Text, c.Sync, action == conversationui.PromptSkip) {
			return true, nil
		}
		if step, submit := u.questions.Advance(); step != 0 {
			u.moveQuestion(step)
		} else if submit {
			return true, u.submitQuestions()
		}
	}
	return true, nil
}
func (u *appServerUI) submitQuestions() error {
	c := u.activeQuestionCall()
	if c == nil || c.Resolved || c.Sent {
		return nil
	}
	defer func() {
		u.pruneDraftImages()
		u.questions.AutoOpen = true
		u.autoOpenQuestions()
	}()
	if c.sudo != nil {
		return u.submitSudoPassword(c)
	}
	attached := questionAnswerAttachments(c.Call, c.secrets, c.secretAnswers)
	if len(c.request) > 0 {
		if err := u.requests.Respond(c.request, conversationbinding.QuestionResponse(c.Call, c.secretAnswers)); err != nil {
			return err
		}
		u.hideQuestions()
		c.Submitted()
		clear(c.secrets)
		clear(c.secretAnswers)
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
	u.hideQuestions()
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
func questionAnswerAttachments(c *prompt.Call, secrets map[int]questionEditor, secretAnswers map[int][]string) composer.Draft {
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
func (u *appServerUI) rejectQuestionParts(parts []conversationmodel.Input) []conversationmodel.Input {
	var ordinary []conversationmodel.Input
	for _, p := range parts {
		c := p.Question
		if c == nil || c.Sync {
			ordinary = append(ordinary, p)
			continue
		}
		if c.Reject() {
			if handle := u.questions.handles[c]; handle != nil {
				u.renderQuestionRecord(handle)
			}
		}
	}
	return ordinary
}
func (u *appServerUI) renderQuestionRecord(c *nativeQuestionCall) {
	if c.sudo != nil {
		return
	}
	states := []string{}
	var body []string
	var records []activityui.Question
	for _, q := range c.View().Questions {
		state := q.State
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
		record := activityui.Question{Text: q.Text, State: state}
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
	if len(c.request) == 0 && !c.Resolved && u.Turn == "" {
		label += " · answer starts a new turn"
	}
	text := label + "\n" + strings.Join(body, "\n\n")
	id := c.Item
	if len(c.request) > 0 {
		id = "question/" + string(c.request)
	}
	for _, v := range []*activityAdapter{u.view, u.agents} {
		if v == nil {
			continue
		}
		entry := activityPaneEntry{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question", Text: text, Observed: u.now(), native: &liveActivityNativeItem{thread: c.Thread, turn: c.Turn, item: id, phase: "question", questions: records}}
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	}
}

// mainTitleDetail follows Main's pane name: unanswered questions, then the
// skills loaded into Main's current context.
func (u *appServerUI) mainTitleDetail() string {
	details := []string{u.questionBadge()}
	if set := u.view.ActiveSkills()["Main"]; set != nil {
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
