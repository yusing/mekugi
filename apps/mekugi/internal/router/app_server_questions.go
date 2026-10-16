package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"

	"encoding/json/jsontext"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/terminalui"

	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/shellapproval"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type nativeQuestionCall struct {
	prompt.Call
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
type questionLayout struct {
	column       *int
	top, textTop int
}
type nativeQuestionDock struct {
	prompt.Questions
	handles map[*prompt.Call]*nativeQuestionCall
	layout  map[*prompt.Question]*questionLayout
	rect    terminalRect
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
func (u *appServerUI) questionLayout(q *prompt.Question) *questionLayout {
	if u.questions.layout == nil {
		u.questions.layout = make(map[*prompt.Question]*questionLayout)
	}
	if u.questions.layout[q] == nil {
		u.questions.layout[q] = &questionLayout{}
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
		u.questionLayout(q).column = e.column
	}
}
func (u *appServerUI) resumeQuestion(q *prompt.Question) {
	if q.IsSecret {
		u.loadQuestionEditor(u.activeQuestionCall().secrets[u.questions.Index])
		return
	}
	u.loadQuestionEditor(questionEditor{q.Editor, u.questionLayout(q).column})
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
	if u.Text != "" || len(u.Images) > 0 || len(u.Files) > 0 || len(u.Skills) > 0 || len(u.Selections) > 0 || u.paste || u.escape != "" || u.pickerVisible() || u.keybindings || u.statusPanel != nil || u.resumePicker != nil || u.approvals.Open {
		return false
	}
	return u.shell == nil || u.shell.focus == 0 && !u.shell.paste && u.shell.sequence == ""
}
func (u *appServerUI) openQuestionCall(c *nativeQuestionCall) {
	if u.questions.Active != nil {
		return
	}
	u.hideApprovals()
	u.questions.painted = false
	u.promptColumn = u.cursorColumn
	u.promptEditor.Open(&u.Editor, composer.State{})
	if !u.questions.Open(&c.Call) {
		return
	}
	u.resumeQuestion(u.currentQuestion())
	u.focusPrompt()
}
func (u *appServerUI) focusPrompt() {
	u.picker.open, u.keybindings = false, false
	u.cancelPickerScan()
	if u.shell != nil {
		u.shell.focus = 0
		u.shell.selection = nil
	}
}
func (u *appServerUI) hideQuestions() {
	if q := u.currentQuestion(); q != nil {
		u.parkQuestion(q)
		u.promptEditor.Close(&u.Editor)
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
	if c.Thread != u.Thread && !c.Local || !u.questions.Add(&c.Call, replay) {
		return
	}
	if u.questions.handles == nil {
		u.questions.handles = make(map[*prompt.Call]*nativeQuestionCall)
	}
	u.questions.handles[&c.Call] = c
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
		u.addQuestions(&nativeQuestionCall{Call: conversationbinding.Questions(p.ThreadID, p.TurnID, p.ItemID, m.ID, p.Questions), request: slices.Clone(m.ID)}, false)
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
func (u *appServerUI) observeQuestionItem(thread, turn string, item appServerItem, replay bool) bool {
	if item.Type != "agentMessage" || item.Delivery != "async" || len(item.Questions) == 0 {
		return false
	}
	u.addQuestions(&nativeQuestionCall{Call: conversationbinding.Questions(thread, turn, item.ID, nil, item.Questions)}, replay)
	return true
}

func (u *appServerUI) saveUnsentAnswer(d composer.Draft) {
	if d.Text != "" {
		u.rememberInput(composerDraft{Draft: d})
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
			u.recordApproval(&nativeApproval{Approval: prompt.Approval{Thread: c.Thread, Turn: c.Turn, Item: c.sudo.item}}, "Denied: sudo authentication canceled")
		}
		u.questions.Remove(&c.Call)
		delete(u.questions.handles, &c.Call)
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
	for _, v := range []*liveActivityView{u.view, u.agents} {
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
func (u *appServerUI) supersedeQuestions(turn string) {
	for _, c := range u.questionCalls() {
		if c.sudo == nil && len(c.request) == 0 && c.Turn != turn && (!c.Sent || c.SubmissionID == "") {
			u.resolveQuestionCall(c, "superseded")
		}
	}
}

func (u *appServerUI) questionKey(key string) (bool, error) {
	q := u.currentQuestion()
	if q == nil {
		return false, nil
	}
	c := u.activeQuestionCall()
	if u.questions.Confirm {
		if key == "\r" {
			return true, u.submitQuestions()
		}
		if key == "\x1b" {
			u.questions.Confirm = false
			return true, nil
		}
	}
	switch key {
	case "\x1b":
		if q.Note {
			q.Note = false
		} else {
			u.questions.AutoOpen = false
			u.hideQuestions()
		}
	case "\x1b[A", "\x10", "\x1b[B", "\x0e":
		step := 1
		if key == "\x1b[A" || key == "\x10" {
			step = -1
		}
		q.Choose((q.Selected + step + len(q.Choices) + 1) % (len(q.Choices) + 1))
	case "\x1b[D":
		if q.Note || q.Selected == len(q.Choices) {
			u.moveDraft(key)
		} else {
			u.moveQuestion(-1)
		}
	case "\x1b[C":
		if q.Note || q.Selected == len(q.Choices) {
			u.moveDraft(key)
		} else {
			u.moveQuestion(1)
		}
	case "\x1b[5~":
		u.questionLayout(q).textTop = max(0, u.questionLayout(q).textTop-1)
	case "\x1b[6~":
		u.questionLayout(q).textTop++
	case "\t":
		if len(c.request) > 0 && q.Selected < len(q.Choices) {
			q.Note = true
		}
	case "\x16":
		q.Edit(!q.Note)
		u.pasteImage()
	case "\r", "\x1d":
		u.parkQuestion(q)
		if q.IsSecret {
			answer := *q
			if !answer.Complete(u.Text, c.Sync, key == "\x1d") {
				return true, nil
			}
			q.Done, q.Skipped = answer.Done, answer.Skipped
			if c.secretAnswers == nil {
				c.secretAnswers = make(map[int][]string)
			}
			c.secretAnswers[u.questions.Index] = answer.Answer
		} else if !q.Complete(u.Text, c.Sync, key == "\x1d") {
			return true, nil
		}
		if step, submit := u.questions.Advance(); step != 0 {
			u.moveQuestion(step)
		} else if submit {
			return true, u.submitQuestions()
		}

	default:
		if c.sudo == nil && len(key) == 1 && key[0] >= '1' && key[0] <= '9' && !q.Note && u.Text == "" {
			selected := int(key[0] - '1')
			if selected <= len(q.Choices) {
				q.Choose(selected)
				return true, nil
			}
		}
		if len(key) == 1 {
			q.Edit(key[0] >= 32 && key[0] != 127 && !q.Note)
		}
		return false, nil
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
	attached := questionAnswerAttachments(c)
	if len(c.request) > 0 {
		if err := u.requests.Respond(c.request, conversationbinding.QuestionResponse(&c.Call, c.secretAnswers)); err != nil {
			return err
		}
		u.hideQuestions()
		c.Submitted()
		clear(c.secrets)
		clear(c.secretAnswers)
		u.renderQuestionRecord(c)
		if len(attached.AnswerImages) > 0 || len(attached.Attachments) > 0 {
			d := composerDraft{Draft: attached}
			d.Text, d.questionCall, d.InHistory = "Attachments accompanying answers to "+c.Item+":\n", c, true
			d.AnswerImages = nil
			for i, path := range attached.AnswerImages {
				label := fmt.Sprintf("[Image %d]", i+1)
				start := len(d.Text)
				d.Text += label + "\n"
				d.Images = append(d.Images, composer.Token{Start: start, End: start + len(label), Path: path})
			}
			u.unsent = append(u.unsent, d)
			return u.flushInput()
		}
		return nil
	}
	reply := conversationbinding.QuestionReplies(&c.Call)
	u.hideQuestions()
	c.Submitted()
	if reply == "" {
		c.Resolved = true
		u.renderQuestionRecord(c)
		return nil
	}
	attached.Text, attached.InHistory = reply, true
	u.unsent = append(u.unsent, composerDraft{Draft: attached, questionCall: c})
	u.renderQuestionRecord(c)
	return u.flushInput()
}

// Answers use composer attachment snapshots. Keep the async reply envelope
// intact while the host processes accompanying images and quoted selections.
func questionAnswerAttachments(c *nativeQuestionCall) composer.Draft {
	var paths []string
	var drafts []composer.Draft
	for i := range c.Questions {
		q := &c.Questions[i]
		if !q.Done || q.Skipped || q.Outcome != "" {
			continue
		}
		d := q.Editor.Snapshot.Draft
		if q.IsSecret {
			d = c.secrets[i].Snapshot.Draft
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
			c.secretAnswers[i] = answer
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
func pendingQuestionReplies(parts []composerDraft) []composerDraft {
	var pending []composerDraft
	for _, part := range parts {
		c := part.questionCall
		if c == nil {
			pending = append(pending, part)
			continue
		}
		if len(c.request) > 0 {
			pending = append(pending, part) // Sync attachments follow the completed tool response.
			continue
		}
		if c.Resolved {
			continue
		}
		attached := questionAnswerAttachments(c)
		part.AnswerImages, part.Attachments, part.AttachmentNotice = attached.AnswerImages, attached.Attachments, attached.AttachmentNotice
		part.Text = conversationbinding.PendingQuestionReply(&c.Call, part.Text)
		if part.Text == "" {
			continue
		}
		pending = append(pending, part)
	}
	return pending
}
func (u *appServerUI) commitQuestionReplies(item appServerItem, turn string) {
	text, _, _ := appServerUserText(item.Content)
	replies := binding.QuestionReplies(text)
	first := u.questions.InputTurn(turn)
	if len(replies) == 0 {
		if first {
			u.supersedeQuestions(turn)
		}
		return
	}
	for _, r := range replies {
		itemID, index, ok := binding.QuestionReplyIdentity(r)
		if !ok {
			continue
		}
		for _, c := range u.questionCalls() {
			if c.Thread != u.Thread || c.Item != itemID || len(c.request) > 0 || index < 0 || index >= len(c.Questions) {
				continue
			}
			q := &c.Questions[index]
			if q.Outcome != "" {
				continue
			}
			wasOpen := u.activeQuestionCall() == c
			if wasOpen {
				u.hideQuestions()
			}
			if discarded, changed := c.Commit(index, item.ClientID, r.Answer); changed {
				u.saveUnsentAnswer(discarded)
			}

			u.renderQuestionRecord(c)
		}
	}
}
func (u *appServerUI) rejectQuestionParts(parts []composerDraft) []composerDraft {
	var ordinary []composerDraft
	for _, p := range parts {
		c := p.questionCall
		if c == nil || len(c.request) > 0 {
			ordinary = append(ordinary, p)
			continue
		}
		if c.Reject() {
			u.renderQuestionRecord(c)
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
	for _, v := range []*liveActivityView{u.view, u.agents} {
		if v == nil {
			continue
		}
		entry := activityPaneEntry{Seq: v.LastSeq + 1, Agent: "Main", Kind: "question", Text: text, Observed: u.now(), native: &liveActivityNativeItem{thread: c.Thread, turn: c.Turn, item: id, phase: "question", questions: records}}
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	}
}
func (u *appServerUI) questionRows(width, height int) []string {
	d := &u.questions
	if u.questionCount() == 0 {
		return nil
	}
	p := &u.view.painter
	accent, dim, reset := p.Theme.Accent(), terminalui.Dim, terminalui.Reset
	if u.activeQuestionCall() == nil {
		label := accent + "? " + reset + fmt.Sprintf("main asks %d questions", u.questionCount()) + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " answer" + reset
		if u.Turn == "" && u.Text != "" {
			label = dim + fmt.Sprintf("enter starts a new turn · dismisses %d questions", u.questionCount()) + reset
		}
		return []string{ansi.Truncate(label, width, "…")}
	}
	q, c := u.currentQuestion(), u.activeQuestionCall()
	view := c.View()
	question := view.Questions[d.Index]
	layout := u.questionLayout(q)
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	state := "open"
	if view.Sync || view.Local {
		state = "waiting"
	}
	head := fmt.Sprintf("? %d of %d", d.Index+1, len(view.Questions))
	if question.Header != question.Text && ansi.StringWidth(head)+ansi.StringWidth(question.Header)+len(state)+6 <= inner {
		head += " · " + pickerText(question.Header)
	}
	gap := inner - ansi.StringWidth(head) - len(state)
	header := accent + "\x1b[1m" + head + reset
	if gap >= 2 {
		header += strings.Repeat(" ", gap) + p.QuestionState(state)
	}
	rows := []string{ansi.Truncate(header, inner, "")}
	if height >= 10 {
		rows = append(rows, "")
	}
	keys := [][2]string{{fmt.Sprintf("1–%d", min(9, len(question.Choices)+1)), "choose"}, {"type", "answer"}}
	if view.Local {
		keys = [][2]string{{"type", "password"}, {"enter", "submit"}}
	}
	arrows := "question"
	if question.Note || question.Selected == len(question.Choices) {
		arrows = "move"
	}
	if view.Sync {
		keys = append(keys, [2]string{"tab", "note"})
	}
	if len(view.Questions) > 1 {
		keys = append(keys, [2]string{"enter", "next"}, [2]string{"←/→", arrows})
	}
	keys = append(keys, [2]string{"ctrl+]", "skip"}, [2]string{"esc", "hide"})
	hints := func(pairs [][2]string) []string {
		var parts []string
		for _, pair := range pairs {
			parts = append(parts, accent+pair[0]+reset+dim+" "+pair[1]+reset)
		}
		return pickerWrap(strings.Join(parts, dim+" · "+reset), inner)
	}
	foot := hints(keys)
	if len(foot) > max(2, height/3) {
		compact := [][2]string{{"↑↓", "pick"}}
		if len(view.Questions) > 1 {
			compact = append(compact, [2]string{"↵", "next"}, [2]string{"←→", arrows})
		}
		foot = hints(append(compact, [2]string{"^]", "skip"}, [2]string{"esc", "hide"}))
	}
	if d.Confirm {
		gaps := c.Gaps()
		foot = pickerWrap(accent+fmt.Sprintf("Submit with %d unanswered?", gaps)+reset+" · "+accent+"enter"+reset+dim+" submit · "+reset+accent+"esc"+reset+dim+" back"+reset, inner)
	}
	text := pickerWrap(textsanitize.Safe(question.Text, false), inner)
	textRoom := max(1, height-len(rows)-len(foot)-5)
	layout.textTop = min(layout.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[layout.textTop:min(len(text), layout.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, dim+ansi.Truncate("pgup/pgdn question text", inner, "")+reset)
	}
	if height >= 10 {
		rows = append(rows, "")
	}
	options := append(slices.Clone(question.Choices), prompt.Option{Label: "Other", Description: "Type your answer"})
	count := min(8, len(options), max(1, height-len(rows)-len(foot)-2))
	layout.top = min(question.Selected, min(layout.top, max(0, len(options)-count)))
	if question.Selected >= layout.top+count {
		layout.top = question.Selected - count + 1
	}
	columns := 0
	for _, o := range options[layout.top:min(len(options), layout.top+count)] {
		columns = max(columns, ansi.StringWidth(pickerText(o.Label)))
	}
	for i := layout.top; i < min(len(options), layout.top+count); i++ {
		o := options[i]
		prefix := "  "
		if i == question.Selected {
			prefix = "› "
		}
		line := fmt.Sprintf("%s%d. %s", prefix, i+1, pickerText(o.Label))
		if inner-columns-7 >= 24 {
			line += strings.Repeat(" ", max(0, columns-ansi.StringWidth(pickerText(o.Label)))+2) + dim + pickerText(o.Description) + reset
		}
		line = ansi.Truncate(line, inner, "…")
		if i == question.Selected {
			line = u.pickerSelection() + ansi.Strip(line) + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + reset
		}
		rows = append(rows, line)
	}
	if count < len(options) {
		rows = append(rows, dim+ansi.Truncate(fmt.Sprintf("option %d/%d", question.Selected+1, len(options)), inner, "")+reset)
	}
	if len(rows)+len(foot) < height {
		rows = append(rows, "")
	}
	rows = append(rows, foot...)
	for i := range rows {
		rows[i] = strings.Repeat(" ", padding) + rows[i]
	}
	return rows[:min(len(rows), height)]
}

// mainTitleDetail follows Main's pane name: unanswered questions, then the
// skills loaded into Main's current context.
func (u *appServerUI) mainTitleDetail() string {
	details := []string{u.questionBadge()}
	if set := u.view.ActiveSkills()["Main"]; set != nil {
		details = append(details, skillLabel(set))
	}
	return strings.Join(slices.DeleteFunc(details, func(detail string) bool { return detail == "" }), terminalui.Dim+" · "+terminalui.Undim)
}

func (u *appServerUI) questionBadge() string {
	if u.shell != nil && u.shell.focus != 0 {
		if n := u.questionCount(); n > 0 {
			return fmt.Sprintf("?%d", n)
		}
	}
	return ""
}
