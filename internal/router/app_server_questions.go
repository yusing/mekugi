package router

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type nativeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}
type nativeQuestion struct {
	ID                     string         `json:"id"`
	Header                 string         `json:"header"`
	Question               string         `json:"question"`
	Title                  string         `json:"title"`
	IsSecret               bool           `json:"isSecret"`
	Options                jsontext.Value `json:"options"`
	choices                []nativeQuestionOption
	editor                 questionEditor
	selected, top, textTop int
	note, done, skipped    bool
	answer                 []string
	outcome                string
}
type nativeQuestionCall struct {
	thread, turn, item string
	request            jsontext.Value
	questions          []nativeQuestion
	sent, resolved     bool
	submissionID       string
}

// A parked editor includes the navigation and undo state, not just its text.
type questionEditor struct {
	snapshot   composerUndo
	undo, redo []composerUndo
	column     *int
	run        composerRun
}
type nativeQuestionDock struct {
	inputTurns map[string]bool
	calls      []*nativeQuestionCall
	active     *nativeQuestionCall
	index      int
	parked     questionEditor
	confirm    bool
	rect       terminalRect
	autoOpen   bool
	painted    bool
}

func (u *appServerUI) saveQuestionEditor() questionEditor {
	return questionEditor{u.undoSnapshot(), u.undoDrafts, u.redoDrafts, u.cursorColumn, u.run}
}
func (u *appServerUI) loadQuestionEditor(e questionEditor) {
	u.loadDraft(e.snapshot.composerDraft)
	u.historyBack, u.historyDraft = e.snapshot.historyBack, e.snapshot.historyDraft
	u.undoDrafts, u.redoDrafts, u.cursorColumn, u.run = e.undo, e.redo, e.column, e.run
}
func (u *appServerUI) currentQuestion() *nativeQuestion {
	d := &u.questions
	if d.active == nil {
		return nil
	}
	return &d.active.questions[d.index]
}
func (u *appServerUI) questionCount() int {
	n := 0
	for _, c := range u.questions.calls {
		if c.thread != u.thread || c.resolved || c.sent {
			continue
		}
		for _, q := range c.questions {
			if q.outcome == "" {
				n++
			}
		}
	}
	return n
}
func (u *appServerUI) waitingQuestion() bool {
	for _, c := range u.questions.calls {
		if c.thread == u.thread && len(c.request) > 0 && !c.resolved {
			return true
		}
	}
	return false
}
func (u *appServerUI) openQuestions() {
	if u.questions.active != nil {
		return
	}
	for _, c := range u.questions.calls {
		if len(c.request) > 0 && !c.resolved && !c.sent {
			u.openQuestionCall(c)
			return
		}
	}
	for _, c := range u.questions.calls {
		if !c.resolved && !c.sent {
			u.openQuestionCall(c)
			return
		}
	}
}

// Only an empty, idle composer yields to pending live questions. Explicitly
// hiding the dock and history restoration leave manual reopening available.
func (u *appServerUI) autoOpenQuestions() {
	if u.questions.autoOpen && u.questions.active == nil && u.composerVacant() {
		u.openQuestions()
	}
}

// composerVacant reports an empty, idle composer with no overlay, which a
// pending dock may take over without capturing keys meant for a draft.
func (u *appServerUI) composerVacant() bool {
	if u.draft != "" || len(u.images) > 0 || len(u.files) > 0 || len(u.skills) > 0 || len(u.selections) > 0 || u.paste || u.escape != "" || u.pickerVisible() || u.keybindings || u.statusPanel != nil || u.resumePicker != nil || u.approvals.open {
		return false
	}
	return u.shell == nil || u.shell.focus == 0 && !u.shell.paste && u.shell.sequence == ""
}
func (u *appServerUI) openQuestionCall(c *nativeQuestionCall) {
	if u.questions.active != nil {
		return
	}
	u.hideApprovals()
	u.questions.painted = false
	u.questions.parked = u.saveQuestionEditor()
	u.questions.active, u.questions.index, u.questions.confirm = c, 0, false
	for i := range c.questions {
		if c.questions[i].outcome == "" {
			u.questions.index = i
			break
		}
	}
	u.loadQuestionEditor(u.currentQuestion().editor)
	u.picker.open, u.keybindings = false, false
	u.cancelPickerScan()
	if u.shell != nil {
		u.shell.focus = 0
		u.shell.selection = nil
	}
}
func (u *appServerUI) hideQuestions() {
	if q := u.currentQuestion(); q != nil {
		q.editor = u.saveQuestionEditor()
		u.loadQuestionEditor(u.questions.parked)
		u.questions.active = nil
		u.questions.confirm = false
	}
}
func (u *appServerUI) moveQuestion(step int) {
	c := u.questions.active
	u.currentQuestion().editor = u.saveQuestionEditor()
	u.questions.index = (u.questions.index + step + len(c.questions)) % len(c.questions)
	u.questions.confirm = false
	u.loadQuestionEditor(u.currentQuestion().editor)
}
func (u *appServerUI) addQuestions(c *nativeQuestionCall, replay bool) {
	if c.thread != u.thread || len(c.questions) == 0 {
		return
	}
	for _, old := range u.questions.calls {
		if old.thread == c.thread && ((len(c.request) > 0 && string(old.request) == string(c.request)) || (len(c.request) == 0 && len(old.request) == 0 && old.item == c.item)) {
			return
		}
	}
	for i := range c.questions {
		q := &c.questions[i]
		if len(c.request) > 0 {
			_ = json.Unmarshal(q.Options, &q.choices)
		} else {
			var labels []string
			_ = json.Unmarshal(q.Options, &labels)
			for _, label := range labels {
				q.choices = append(q.choices, nativeQuestionOption{Label: label})
			}
		}
		q.Question = cmp.Or(q.Question, q.Title)
		q.Header = cmp.Or(q.Header, strings.Split(q.Question, "\n")[0])
	}
	if u.questions.inputTurns == nil {
		u.questions.inputTurns = make(map[string]bool)
	}
	u.questions.inputTurns[c.turn] = true
	u.questions.calls = append(u.questions.calls, c)
	if !replay {
		if len(c.request) > 0 {
			u.notify("plan-mode-prompt", "Input requested")
		} else {
			u.notify("async-question", "Question pending")
		}
	}
	u.renderQuestionRecord(c)
	if !replay {
		u.questions.autoOpen = true
		u.autoOpenQuestions()
	}
}
func (u *appServerUI) questionMessage(m appserver.Message) (bool, error) {
	if m.Method == "item/tool/requestUserInput" && len(m.ID) > 0 {
		var p struct {
			ThreadID  string           `json:"threadId"`
			TurnID    string           `json:"turnId"`
			ItemID    string           `json:"itemId"`
			Questions []nativeQuestion `json:"questions"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return true, err
		}
		u.addQuestions(&nativeQuestionCall{thread: p.ThreadID, turn: p.TurnID, item: p.ItemID, request: slices.Clone(m.ID), questions: p.Questions}, false)
		return true, nil
	}
	if m.Method == "serverRequest/resolved" {
		var p struct {
			ThreadID  string         `json:"threadId"`
			RequestID jsontext.Value `json:"requestId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return true, err
		}
		for _, c := range u.questions.calls {
			if c.thread == p.ThreadID && string(c.request) == string(p.RequestID) {
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
	u.addQuestions(&nativeQuestionCall{thread: thread, turn: turn, item: item.ID, questions: item.Questions}, replay)
	return true
}
func (u *appServerUI) saveUnsentAnswer(q *nativeQuestion) {
	if !q.IsSecret && q.editor.snapshot.text != "" {
		u.rememberInput(q.editor.snapshot.composerDraft)
		u.setNotice("unsent answer saved to input history", false)
	}
	q.editor = questionEditor{}
}
func (u *appServerUI) resolveQuestionCall(c *nativeQuestionCall, outcome string) {
	if c.resolved {
		return
	}
	if u.questions.active == c {
		u.hideQuestions()
	}
	for i := range c.questions {
		q := &c.questions[i]
		if q.outcome == "" {
			q.outcome = outcome
			if len(c.request) > 0 && c.sent {
				if q.skipped || !q.done {
					q.outcome = "skipped"
				} else {
					q.outcome = "answered"
				}
			} else {
				u.saveUnsentAnswer(q)
			}
			q.editor = questionEditor{}
		}
	}
	c.resolved = true
	u.renderQuestionRecord(c)
	if len(c.request) > 0 && c.sent {
		u.recordSyncQuestionAnswer(c)
	}
}

// Synchronous answers have no user-message envelope. Retain their committed
// response as the same hidden chronological anchor used by async answers.
func (u *appServerUI) recordSyncQuestionAnswer(c *nativeQuestionCall) {
	var answers []string
	for _, q := range c.questions {
		if q.outcome != "answered" {
			continue
		}
		if q.IsSecret {
			answers = append(answers, "•••")
			continue
		}
		for i, answer := range q.answer {
			if i > 0 || q.selected == len(q.choices) {
				answer = strings.TrimPrefix(answer, "user_note: ")
			}
			answers = append(answers, answer)
		}
	}
	if len(answers) == 0 {
		return
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		if v == nil {
			continue
		}
		native := &liveActivityNativeItem{thread: c.thread, turn: c.turn, item: "question-answer/" + string(c.request), phase: "item/completed"}
		for _, previous := range slices.Backward(v.entries) {
			if previous.native == nil || previous.native.thread != c.thread {
				continue
			}
			if previous.Kind == "question" && previous.native.item == "question/"+string(c.request) {
				native.question = previous.Seq
			}
			if native.replySource == "" && previous.Agent == "You" && previous.native.turn == c.turn {
				native.replySource = previous.Text
			}
		}
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.lastSeq + 1, Agent: "Main", Kind: "question_reply", Text: strings.Join(answers, "\n"), Observed: u.now(), native: native}}})
	}
}
func (u *appServerUI) endSyncQuestions(turn string) {
	for _, c := range u.questions.calls {
		if c.turn == turn && len(c.request) > 0 {
			u.resolveQuestionCall(c, "interrupted")
		}
	}
}
func (u *appServerUI) supersedeQuestions(turn string) {
	for _, c := range u.questions.calls {
		if len(c.request) == 0 && c.turn != turn && (!c.sent || c.submissionID == "") {
			u.resolveQuestionCall(c, "superseded")
		}
	}
}

func (u *appServerUI) questionKey(key string) (bool, error) {
	q := u.currentQuestion()
	if q == nil {
		return false, nil
	}
	c := u.questions.active
	if u.questions.confirm {
		if key == "\r" {
			return true, u.submitQuestions()
		}
		if key == "\x1b" {
			u.questions.confirm = false
			return true, nil
		}
	}
	switch key {
	case "\x1b":
		if q.note {
			q.note = false
		} else {
			u.questions.autoOpen = false
			u.hideQuestions()
		}
	case "\x1b[A", "\x10", "\x1b[B", "\x0e":
		step := 1
		if key == "\x1b[A" || key == "\x10" {
			step = -1
		}
		q.selected = (q.selected + step + len(q.choices) + 1) % (len(q.choices) + 1)
		q.done = false
	case "\x1b[D":
		if q.note || q.selected == len(q.choices) {
			u.moveDraft(key)
		} else {
			u.moveQuestion(-1)
		}
	case "\x1b[C":
		if q.note || q.selected == len(q.choices) {
			u.moveDraft(key)
		} else {
			u.moveQuestion(1)
		}
	case "\x1b[5~":
		q.textTop = max(0, q.textTop-1)
	case "\x1b[6~":
		q.textTop++
	case "\t":
		if len(c.request) > 0 && q.selected < len(q.choices) {
			q.note = true
		}
	case "\x16":
		if !q.note {
			q.selected = len(q.choices)
		}
		q.done, q.skipped = false, false
		u.pasteImage()
	case "\r", "\x1d":
		q.editor = u.saveQuestionEditor()
		q.skipped = key == "\x1d"
		q.done = true
		q.answer = nil
		if !q.skipped {
			if q.selected < len(q.choices) {
				q.answer = []string{q.choices[q.selected].Label}
				if len(c.request) > 0 && u.draft != "" {
					q.answer = append(q.answer, "user_note: "+u.draft)
				}
			} else if u.draft != "" {
				answer := u.draft
				if len(c.request) > 0 {
					answer = "user_note: " + answer
				}
				q.answer = []string{answer}
			} else {
				q.done = false
				return true, nil
			}
		}
		for j := 1; u.questions.index+j < len(c.questions); j++ {
			next := u.questions.index + j
			if !c.questions[next].done && c.questions[next].outcome == "" {
				u.moveQuestion(j)
				return true, nil
			}
		}
		gaps := 0
		for _, question := range c.questions {
			if question.outcome == "" && (question.skipped || !question.done) {
				gaps++
			}
		}
		if gaps > 0 {
			u.questions.confirm = true
		} else {
			return true, u.submitQuestions()
		}
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' && !q.note && u.draft == "" {
			selected := int(key[0] - '1')
			if selected <= len(q.choices) {
				q.selected = selected
				q.done = false
				return true, nil
			}
		}
		if len(key) == 1 && key[0] >= 32 && key[0] != 127 && !q.note {
			q.selected = len(q.choices)
		}
		if len(key) == 1 {
			q.done = false
			q.skipped = false
		}
		return false, nil
	}
	return true, nil
}
func (u *appServerUI) submitQuestions() error {
	c := u.questions.active
	if c == nil || c.resolved || c.sent {
		return nil
	}
	defer func() {
		u.pruneDraftImages()
		u.questions.autoOpen = true
		u.autoOpenQuestions()
	}()
	images := questionAnswerImages(c)
	if len(c.request) > 0 {
		answers := make(map[string]map[string][]string)
		for _, q := range c.questions {
			if q.done && !q.skipped {
				answers[q.ID] = map[string][]string{"answers": q.answer}
			}
		}
		if err := u.client.Respond(c.request, map[string]any{"answers": answers}); err != nil {
			return err
		}
		c.sent = true
		u.hideQuestions()
		for i := range c.questions {
			c.questions[i].editor = questionEditor{}
		}
		u.renderQuestionRecord(c)
		if len(images) > 0 {
			d := composerDraft{text: "Images accompanying answers to " + c.item + ":\n", questionCall: c, inHistory: true}
			for i, path := range images {
				label := fmt.Sprintf("[Image %d]", i+1)
				start := len(d.text)
				d.text += label + "\n"
				d.images = append(d.images, composerImage{start, start + len(label), path})
			}
			u.unsent = append(u.unsent, d)
			return u.flushInput()
		}
		return nil
	}
	var replies []nativeQuestionReply
	for i := range c.questions {
		q := &c.questions[i]
		if q.outcome != "" {
			continue
		}
		if q.skipped || !q.done {
			q.outcome = "skipped"
			continue
		}
		id, _ := json.Marshal([]any{"request_user_input_async", c.item, i})
		replies = append(replies, nativeQuestionReply{Answer: strings.Join(q.answer, "\n"), Question: q.Question, QuestionItemID: string(id)})
	}
	u.hideQuestions()
	for i := range c.questions {
		if c.questions[i].outcome == "skipped" {
			c.questions[i].editor = questionEditor{}
		}
	}
	c.sent = true
	if len(replies) == 0 {
		c.resolved = true
		u.renderQuestionRecord(c)
		return nil
	}
	data, _ := json.Marshal(replies)
	u.unsent = append(u.unsent, composerDraft{text: questionReplyStart + string(data) + questionReplyEnd, answerImages: images, questionCall: c, inHistory: true})
	u.renderQuestionRecord(c)
	return u.flushInput()
}

// Answers refer to the same labels as composer images. The async envelope stays
// a complete text part; the host processes its accompanying localImage inputs.
func questionAnswerImages(c *nativeQuestionCall) []string {
	var paths []string
	for i := range c.questions {
		q := &c.questions[i]
		if !q.done || q.skipped || q.outcome != "" {
			continue
		}
		d := q.editor.snapshot.composerDraft
		if len(d.images) == 0 {
			continue
		}
		text := d.text
		for j := len(d.images) - 1; j >= 0; j-- {
			image := d.images[j]
			text = text[:image.start] + fmt.Sprintf("[Image %d]", len(paths)+j+1) + text[image.end:]
		}
		for _, image := range d.images {
			paths = append(paths, image.path)
		}
		q.answer = []string{text}
		if q.selected < len(q.choices) {
			q.answer = []string{q.choices[q.selected].Label, "user_note: " + text}
		} else if len(c.request) > 0 {
			q.answer[0] = "user_note: " + text
		}
	}
	return paths
}

const questionReplyStart = "<send_user_message_question_reply>"
const questionReplyEnd = "</send_user_message_question_reply>"

type nativeQuestionReply struct {
	Answer         string `json:"answer"`
	Question       string `json:"question"`
	QuestionItemID string `json:"questionItemId"`
}

// Match the whole stock payload, never an envelope quoted inside a prompt.
func questionReplies(text string) []nativeQuestionReply {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "# Context from my IDE setup:\n") {
		_, request, ok := strings.CutLast(text, "\n## My request for Codex:\n")
		if !ok {
			return nil
		}
		text = strings.TrimSpace(request)
	}
	body, ok := strings.CutPrefix(text, questionReplyStart)
	if !ok {
		return nil
	}
	body, ok = strings.CutSuffix(body, questionReplyEnd)
	if !ok {
		return nil
	}
	type wireReply struct {
		Answer   *string `json:"answer"`
		Question *string `json:"question"`
		ID       *string `json:"questionItemId"`
	}
	var wire []wireReply
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		var one wireReply
		if json.Unmarshal([]byte(body), &one) != nil {
			return nil
		}
		wire = []wireReply{one}
	} else if json.Unmarshal([]byte(body), &wire) != nil {
		return nil
	}
	var replies []nativeQuestionReply
	for _, r := range wire {
		if r.Answer == nil || r.Question == nil || r.ID == nil {
			return nil
		}
		replies = append(replies, nativeQuestionReply{*r.Answer, *r.Question, *r.ID})
	}
	return replies
}
func questionReplyIdentity(reply nativeQuestionReply) (string, int, bool) {
	var id []jsontext.Value
	if json.Unmarshal([]byte(reply.QuestionItemID), &id) != nil || len(id) != 3 {
		return "", 0, false
	}
	var tool, item string
	var index int
	if json.Unmarshal(id[0], &tool) != nil || tool != "request_user_input_async" || json.Unmarshal(id[1], &item) != nil || json.Unmarshal(id[2], &index) != nil || index < 0 {
		return "", 0, false
	}
	return item, index, true
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
			pending = append(pending, part) // Sync images follow the completed tool response.
			continue
		}
		if c.resolved {
			continue
		}
		part.answerImages = questionAnswerImages(c)
		var replies []nativeQuestionReply
		for _, r := range questionReplies(part.text) {
			item, index, ok := questionReplyIdentity(r)
			if ok && item == c.item && index < len(c.questions) && c.questions[index].outcome == "" {
				r.Answer = strings.Join(c.questions[index].answer, "\n")
				replies = append(replies, r)
			}
		}
		if len(replies) == 0 {
			continue
		}
		data, _ := json.Marshal(replies)
		part.text = questionReplyStart + string(data) + questionReplyEnd
		pending = append(pending, part)
	}
	return pending
}
func (u *appServerUI) commitQuestionReplies(item appServerItem, turn string) {
	text, _, _ := appServerUserText(item.Content)
	replies := questionReplies(text)
	if u.questions.inputTurns == nil {
		u.questions.inputTurns = make(map[string]bool)
	}
	first := !u.questions.inputTurns[turn]
	u.questions.inputTurns[turn] = true
	if len(replies) == 0 {
		if first {
			u.supersedeQuestions(turn)
		}
		return
	}
	for _, r := range replies {
		itemID, index, ok := questionReplyIdentity(r)
		if !ok {
			continue
		}
		for _, c := range u.questions.calls {
			if c.thread != u.thread || c.item != itemID || len(c.request) > 0 || index < 0 || index >= len(c.questions) {
				continue
			}
			q := &c.questions[index]
			if q.outcome != "" {
				continue
			}
			wasOpen := u.questions.active == c
			if wasOpen {
				u.hideQuestions()
			}
			outcome := "answered elsewhere"
			if c.submissionID != "" && (item.ClientID == c.submissionID || item.ClientID == "" && r.Answer == strings.Join(q.answer, "\n")) {
				outcome = "answered"
			} else {
				q.done = false
				u.saveUnsentAnswer(q)
			}
			q.outcome, q.answer = outcome, []string{r.Answer}
			q.editor = questionEditor{}
			c.resolved = true
			for _, other := range c.questions {
				if other.outcome == "" {
					c.resolved = false
				}
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
		if !c.resolved {
			c.sent = false
			c.submissionID = ""
			u.renderQuestionRecord(c)
		}
	}
	return ordinary
}
func (u *appServerUI) renderQuestionRecord(c *nativeQuestionCall) {
	states := []string{}
	var body []string
	var records []activityui.Question
	for _, q := range c.questions {
		state := q.outcome
		if state == "" {
			if c.sent {
				state = "sending"
			} else if len(c.request) > 0 {
				state = "waiting"
			} else {
				state = "open"
			}
		}
		if !slices.Contains(states, state) {
			states = append(states, state)
		}
		record := activityui.Question{Text: q.Question, State: state}
		line := q.Question
		if len(q.answer) > 0 && q.outcome != "" && !q.IsSecret {
			answers := slices.Clone(q.answer)
			for i, answer := range answers {
				if note, ok := strings.CutPrefix(answer, "user_note: "); ok && len(c.request) > 0 && (i > 0 || q.selected == len(q.choices)) {
					if len(answers) == 1 {
						answers[i] = "Other: " + note
					} else {
						answers[i] = note
					}
				}
			}
			record.Answer = strings.Join(answers, " · ")
			line += "\n└ " + record.Answer
		} else if q.outcome == "" && len(q.choices) > 0 {
			labels := []string{}
			for _, o := range q.choices {
				labels = append(labels, o.Label)
			}
			record.Options = strings.Join(labels, " · ")
			line += "\n" + record.Options
		}
		body = append(body, line)
		records = append(records, record)
	}
	label := fmt.Sprintf("%d questions · %s", len(c.questions), strings.Join(states, ", "))
	if len(c.questions) == 1 {
		label = strings.Replace(label, "questions", "question", 1)
	}
	if len(c.request) == 0 && !c.resolved && u.turn == "" {
		label += " · answer starts a new turn"
	}
	text := label + "\n" + strings.Join(body, "\n\n")
	id := c.item
	if len(c.request) > 0 {
		id = "question/" + string(c.request)
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		if v == nil {
			continue
		}
		entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "question", Text: text, Observed: u.now(), native: &liveActivityNativeItem{thread: c.thread, turn: c.turn, item: id, phase: "question", questions: records}}
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	}
}
func (u *appServerUI) questionRows(width, height int) []string {
	d := &u.questions
	if u.questionCount() == 0 {
		return nil
	}
	p := &u.view.painter
	accent, dim, reset := p.Theme.Accent(), activityui.Dim, activityui.Reset
	if d.active == nil {
		label := accent + "? " + reset + fmt.Sprintf("main asks %d questions", u.questionCount()) + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " answer" + reset
		if u.turn == "" && u.draft != "" {
			label = dim + fmt.Sprintf("enter starts a new turn · dismisses %d questions", u.questionCount()) + reset
		}
		return []string{ansi.Truncate(label, width, "…")}
	}
	q, c := u.currentQuestion(), d.active
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	state := "open"
	if len(c.request) > 0 {
		state = "waiting"
	}
	head := fmt.Sprintf("? %d of %d", d.index+1, len(c.questions))
	if q.Header != q.Question && ansi.StringWidth(head)+ansi.StringWidth(q.Header)+len(state)+6 <= inner {
		head += " · " + pickerText(q.Header)
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
	keys := [][2]string{{fmt.Sprintf("1–%d", min(9, len(q.choices)+1)), "choose"}, {"type", "answer"}}
	arrows := "question"
	if q.note || q.selected == len(q.choices) {
		arrows = "move"
	}
	if len(c.request) > 0 {
		keys = append(keys, [2]string{"tab", "note"})
	}
	if len(c.questions) > 1 {
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
		if len(c.questions) > 1 {
			compact = append(compact, [2]string{"↵", "next"}, [2]string{"←→", arrows})
		}
		foot = hints(append(compact, [2]string{"^]", "skip"}, [2]string{"esc", "hide"}))
	}
	if d.confirm {
		gaps := 0
		for _, question := range c.questions {
			if question.outcome == "" && (question.skipped || !question.done) {
				gaps++
			}
		}
		foot = pickerWrap(accent+fmt.Sprintf("Submit with %d unanswered?", gaps)+reset+" · "+accent+"enter"+reset+dim+" submit · "+reset+accent+"esc"+reset+dim+" back"+reset, inner)
	}
	text := pickerWrap(livediff.Safe(q.Question, false), inner)
	textRoom := max(1, height-len(rows)-len(foot)-5)
	q.textTop = min(q.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[q.textTop:min(len(text), q.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, dim+ansi.Truncate("pgup/pgdn question text", inner, "")+reset)
	}
	if height >= 10 {
		rows = append(rows, "")
	}
	options := append(slices.Clone(q.choices), nativeQuestionOption{Label: "Other", Description: "Type your answer"})
	count := min(8, len(options), max(1, height-len(rows)-len(foot)-2))
	q.top = min(q.selected, min(q.top, max(0, len(options)-count)))
	if q.selected >= q.top+count {
		q.top = q.selected - count + 1
	}
	columns := 0
	for _, o := range options[q.top:min(len(options), q.top+count)] {
		columns = max(columns, ansi.StringWidth(pickerText(o.Label)))
	}
	for i := q.top; i < min(len(options), q.top+count); i++ {
		o := options[i]
		prefix := "  "
		if i == q.selected {
			prefix = "› "
		}
		line := fmt.Sprintf("%s%d. %s", prefix, i+1, pickerText(o.Label))
		if inner-columns-7 >= 24 {
			line += strings.Repeat(" ", max(0, columns-ansi.StringWidth(pickerText(o.Label)))+2) + dim + pickerText(o.Description) + reset
		}
		line = ansi.Truncate(line, inner, "…")
		if i == q.selected {
			line = u.pickerSelection() + ansi.Strip(line) + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + reset
		}
		rows = append(rows, line)
	}
	if count < len(options) {
		rows = append(rows, dim+ansi.Truncate(fmt.Sprintf("option %d/%d", q.selected+1, len(options)), inner, "")+reset)
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
	if set := u.view.activeSkills()["Main"]; set != nil {
		details = append(details, set.label())
	}
	return strings.Join(slices.DeleteFunc(details, func(detail string) bool { return detail == "" }), activityui.Dim+" · "+activityui.Undim)
}

func (u *appServerUI) questionBadge() string {
	if u.shell != nil && u.shell.focus != 0 {
		if n := u.questionCount(); n > 0 {
			return fmt.Sprintf("?%d", n)
		}
	}
	return ""
}
