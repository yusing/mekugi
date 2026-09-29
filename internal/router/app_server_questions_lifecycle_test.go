package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
)

func TestQuestionQueuedSteerIsNotStillActionable(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.turn = "turn"
	questionTestAsync(t, u, "first", "Who receives it?")
	questionTestAsync(t, u, "second", "Who approves it?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "3Customers only\r")
	request := appServerOneRequest(t, wire, "turn/steer", u.submission.text)
	u.hideQuestions()
	if u.questionCount() != 1 || u.questions.calls[0].resolved || !u.questions.calls[0].sent {
		t.Fatalf("sent answer and separate unanswered question confused: count=%d call=%+v", u.questionCount(), u.questions.calls[0])
	}
	if rows := strings.Join(u.questionRows(80, 10), "\n"); !strings.Contains(rows, "main asks 1 questions") || strings.Contains(rows, "main asks 2 questions") {
		t.Fatalf("question dock counted the sent answer: %q", rows)
	}
	preview := strings.Join(u.pendingInputPreview(80), "\n")
	if !strings.Contains(preview, "Steering after the next tool call") || !strings.Contains(preview, "Answering 1 question(s)") || strings.Contains(preview, questionReplyStart) {
		t.Fatalf("queued answer was not shown truthfully: %q", preview)
	}
	// RPC acceptance is not a committed user message. The answer remains in-flight.
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "other-answer", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": lifecycleReply(t, "second", 0, "Who approves it?", "Internal")}},
	}})
	if u.questionCount() != 0 || len(u.questionRows(80, 10)) != 0 || u.questions.calls[0].resolved {
		t.Fatal("sent answer remained actionable after the other question was answered")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"turn"}}`, request.ID))
	if u.questions.calls[0].resolved || !strings.Contains(strings.Join(u.pendingInputPreview(80), "\n"), "Answering 1 question(s)") {
		t.Fatal("steer acknowledgment incorrectly completed or hid the uncommitted answer")
	}
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "answer", "clientId": request.Params.ClientUserMessageID, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": request.text()}},
	}})
	if !u.questions.calls[0].resolved || strings.Contains(strings.Join(u.pendingInputPreview(80), "\n"), "Answering 1 question(s)") {
		t.Fatal("committed answer was not settled")
	}
}

func TestQuestionRejectedQueuedSteerReopensDockWithDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.turn = "turn"
	questionTestAsync(t, u, "pending", "Who receives it?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "3Customers only\r")
	request := appServerOneRequest(t, wire, "turn/steer", u.submission.text)
	if u.questionCount() != 0 || len(u.questionRows(80, 10)) != 0 {
		t.Fatal("queued answer still required action")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, request.ID))
	if u.questionCount() != 1 || u.questions.calls[0].sent || u.questions.calls[0].resolved {
		t.Fatal("rejected answer did not become actionable again")
	}
	u.hideQuestions()
	if rows := strings.Join(u.questionRows(80, 10), "\n"); !strings.Contains(rows, "main asks 1 questions") {
		t.Fatalf("reopened question missing from dock: %q", rows)
	}
	u.openQuestions()
	questionTestPaint(t, u, 70)
	if u.draft != "Customers only" {
		t.Fatalf("rejected answer lost its draft: %q", u.draft)
	}
}

func lifecycleReply(t *testing.T, item string, index int, question, answer string) string {
	t.Helper()
	id, err := json.Marshal([]any{"request_user_input_async", item, index})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]nativeQuestionReply{{QuestionItemID: string(id), Question: question, Answer: answer}})
	if err != nil {
		t.Fatal(err)
	}
	return questionReplyStart + string(data) + questionReplyEnd
}

func lifecycleUserMessage(t *testing.T, u *appServerUI, turn, item, text string) {
	t.Helper()
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
		"id": item, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text}},
	}})
}

func TestQuestionReplyEnvelopeMustBeWholePrompt(t *testing.T) {
	reply := lifecycleReply(t, "item", 0, "Who receives it?", "Customers")
	for _, valid := range []string{reply, " \n" + reply + "\n ", "# Context from my IDE setup:\neditor state\n## My request for Codex:\n" + reply} {
		if got := questionReplies(valid); len(got) != 1 || got[0].Answer != "Customers" {
			t.Fatalf("complete envelope not recognized: %q => %#v", valid, got)
		}
	}
	for _, ordinary := range []string{"quoted " + reply, reply + " trailing request", "> " + reply, "`" + reply + "`"} {
		if got := questionReplies(ordinary); len(got) != 0 {
			t.Fatalf("ordinary prompt became a reply: %q => %#v", ordinary, got)
		}
	}
	single := strings.Replace(strings.Replace(reply, "[{", "{", 1), "}]", "}", 1)
	if got := questionReplies(single); len(got) != 1 || got[0].Answer != "Customers" {
		t.Fatalf("single-object stock reply not recognized: %#v", got)
	}
}

func TestQuestionAsyncRemainingSurvivesReplyTurnSteerAndReplay(t *testing.T) {
	u, _ := newAppServerTestUI()
	questionTestAsync(t, u, "two", "Who receives it?", "Who approves it?")
	reply := lifecycleReply(t, "two", 0, "Who receives it?", "Customers")
	lifecycleUserMessage(t, u, "answer-turn", "reply", reply)
	lifecycleUserMessage(t, u, "answer-turn", "steer", "ordinary same-turn steer")
	if u.questionCount() != 1 || u.questions.calls[0].questions[1].outcome != "" {
		t.Fatal("steer in reply turn superseded remaining question")
	}
	// Replay is history-only. It must recover the remaining open question without resending.
	v, input := newAppServerTestUI()
	v.agents = newLiveActivityView()
	v.session.start("main", t.TempDir())
	first, _ := json.Marshal(map[string]any{"id": "two", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Who receives it?", "questions": []any{
		map[string]any{"title": "Who receives it?", "options": []string{"Customers", "Internal"}}, map[string]any{"title": "Who approves it?", "options": []string{"Customers", "Internal"}},
	}})
	var questionItem appServerItem
	if err := json.Unmarshal(first, &questionItem); err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(map[string]any{"id": "reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": reply}}})
	var replyItem appServerItem
	if err := json.Unmarshal(second, &replyItem); err != nil {
		t.Fatal(err)
	}
	v.restoreHistory([]appServerHistoryTurn{{ID: "ask-turn", Status: "completed", Items: []appServerItem{questionItem}}, {ID: "answer-turn", Status: "completed", Items: []appServerItem{replyItem}}})
	if v.questionCount() != 1 || input.Len() != 0 {
		t.Fatalf("replay pending=%d outgoing=%q", v.questionCount(), input.String())
	}
}

func TestQuestionQueuedBatchDropsExternallyResolvedReplies(t *testing.T) {
	for _, externalCount := range []int{1, 2} {
		t.Run(string(rune('0'+externalCount)), func(t *testing.T) {
			u, input := newAppServerTestUI()
			u.turn = "turn"
			questionTestAsync(t, u, "queued", "Who receives it?", "Who approves it?")
			u.starting = true // Another pending turn/start prevents flushing.
			u.openQuestions()
			questionTestPaint(t, u, 70)
			appServerTestKeys(t, u, "1\r1\r")
			questionTestSync(t, u, "blocking", false)
			u.starting = false
			if strings.Contains(input.String(), "turn/steer") {
				t.Fatal("question batch escaped while sync request blocked turn")
			}
			preview := strings.Join(u.pendingInputPreview(80), "\n")
			if strings.Contains(preview, questionReplyStart) || !strings.Contains(preview, "Answering 2 question(s)") {
				t.Fatalf("pending preview exposed reply framing or omitted batch: %q", preview)
			}
			for index := range externalCount {
				lifecycleUserMessage(t, u, "turn", "external", lifecycleReply(t, "queued", index, []string{"Who receives it?", "Who approves it?"}[index], "Customers"))
			}
			questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "blocking"})
			if externalCount == 2 && strings.Contains(input.String(), "turn/steer") {
				t.Fatal("fully resolved batch was resent")
			}
			if externalCount == 1 && !strings.Contains(input.String(), "turn/steer") {
				t.Fatal("remaining question was not flushed")
			}
		})
	}
}

func TestQuestionAnswerOnlyAppearsUnderAsked(t *testing.T) {
	for _, turn := range []string{"", "turn"} {
		t.Run("active="+turn, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.turn = turn
			questionTestAsync(t, u, "echo", "Who receives it?")
			u.openQuestions()
			questionTestPaint(t, u, 70)
			appServerTestKeys(t, u, "2\r")
			reply, id := u.submission.text, u.submission.id
			if len(questionReplies(reply)) != 1 {
				t.Fatal("reply not submitted")
			}
			for _, entry := range u.view.entries {
				if entry.Agent == "You" {
					t.Fatal("pending reply created duplicate user band")
				}
			}
			questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "reply-turn", "item": map[string]any{
				"id": "commit", "clientId": id, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": reply}},
			}})
			frame := questionTestPaint(t, u, 70)
			if strings.Count(frame, "Who receives it?") != 1 || strings.Count(frame, "Internal") != 1 || !strings.Contains(frame, "answered") {
				t.Fatalf("duplicate or missing answer:\n%s", frame)
			}
			for _, entry := range u.view.entries {
				if entry.Agent == "You" {
					t.Fatal("committed reply created duplicate user band")
				}
			}
		})
	}
}

func TestQuestionNewPromptSupersedesUnsentAnswer(t *testing.T) {
	u, w := newAppServerTestUI()
	u.turn = "turn"
	u.starting = true
	questionTestAsync(t, u, "pending-start", "Who receives it?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "3Local draft\r")
	lifecycleUserMessage(t, u, "ordinary-new-turn", "new-prompt", "Do something else")
	u.starting = false
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	if w.Len() != 0 || u.questions.calls[0].questions[0].outcome != "superseded" {
		t.Fatal("superseded local answer was sent or claimed answered")
	}
}

func TestQuestionRecordPreservesLiteralNotePrefix(t *testing.T) {
	for _, sync := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.turn = "turn"
		literal := "user_note: preserve this literal"
		if sync {
			questionTestMessage(t, u, "literal", "item/tool/requestUserInput", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "literal", "questions": []any{map[string]any{
				"id": "label", "question": "Pick a literal label?", "options": []any{map[string]any{"label": literal, "description": "Literal option"}},
			}}})
		} else {
			questionTestAsync(t, u, "literal", "Provide literal text?")
		}
		u.openQuestions()
		questionTestPaint(t, u, 70)
		if sync {
			appServerTestKeys(t, u, "1\r")
			questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "literal"})
		} else {
			appServerTestKeys(t, u, "3"+literal+"\r")
			lifecycleUserMessage(t, u, "turn", "reply", u.submission.text)
		}
		entry := u.view.entries[0]
		if got := entry.native.questions[0].Answer; got != literal {
			t.Fatalf("sync=%v: displayed %q, want literal %q", sync, got, literal)
		}
		questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": appServerItem{ID: "ack", Type: "agentMessage", Text: "I will use that answer."}})
		u.view.conversation = true
		if frame := mainFeed(u, 100); !strings.Contains(frame, "re: your answer") || strings.Count(frame, literal) != 2 {
			t.Fatalf("sync=%v: reply changed the literal answer:\n%s", sync, frame)
		}
	}
}

func TestQuestionReplyBecomesMainReplyAnchor(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "resume"}[replay], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = true
			u.turn = "turn"
			content := func(text string) []byte {
				data, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			items := []appServerItem{
				{ID: "prompt", Type: "userMessage", Content: content("Original task")},
				{ID: "ask", Type: "agentMessage", Delivery: "async", Questions: []nativeQuestion{{Title: "Which audience?"}}},
				{ID: "reply", Type: "userMessage", Content: content(lifecycleReply(t, "ask", 0, "Which audience?", "Customers"))},
				{ID: "ack", Type: "agentMessage", Text: "I will notify them."},
			}
			if replay {
				u.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: items}})
			} else {
				for _, item := range items {
					questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
				}
			}
			var reply, ack activityPaneEntry
			for _, entry := range u.view.entries {
				if entry.Kind == "question_reply" {
					reply = entry
				}
				if entry.native != nil && entry.native.item == "ack" {
					ack = entry
				}
			}
			if reply.Seq == 0 || ack.native == nil || ack.native.question != reply.Seq || reply.Text != "Customers" {
				t.Fatalf("reply not bound to answer: reply=%+v ack=%+v", reply, ack)
			}
			frame := mainFeed(u, 100)
			if !strings.Contains(frame, "re: your answer") || strings.Contains(frame, "re: your message") || strings.Contains(frame, questionReplyStart) {
				t.Fatalf("incorrect reply context:\n%s", frame)
			}
			if _, ok := u.view.questionRows[reply.Seq]; !ok {
				t.Fatal("answer link has no Asked navigation target")
			}
		})
	}
}

func TestQuestionReplyJournalUsesAnswerAnchor(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	questionTestAsync(t, u, "ask", "Which audience?")
	envelope := lifecycleReply(t, "ask", 0, "Which audience?", "Customers")
	lifecycleUserMessage(t, u, "turn", "reply", envelope)
	publication := nativeJournalPublication{item: journalItem{ID: "answer", Text: "I will notify them.", Question: envelope}, terminal: true, batch: 1}
	u.view.applyJournal("main", publication)
	assert := func() {
		t.Helper()
		frame := mainFeed(u, 100)
		if strings.Contains(frame, questionReplyStart) || !strings.Contains(frame, "re: your answer") || strings.Contains(frame, "not loaded") {
			t.Fatalf("journal answer context incorrect:\n%s", frame)
		}
	}
	assert()
	// An identical answer in another call must not move a published link.
	var original uint64
	for _, entry := range u.view.entries {
		if entry.Kind == "question_reply" {
			original = entry.Seq
		}
	}
	questionTestAsync(t, u, "later", "Which audience?")
	lifecycleUserMessage(t, u, "turn", "later-reply", lifecycleReply(t, "later", 0, "Which audience?", "Customers"))
	publication.item.Text = "I will notify the selected audience."
	u.view.applyJournal("main", publication)
	assert()
	for _, blocks := range u.view.blocks {
		for _, block := range blocks {
			if block.Journal != nil {
				for _, group := range block.Journal.Groups {
					if group.Target != original {
						t.Fatalf("journal target moved: %d != %d", group.Target, original)
					}
				}
			}
		}
	}
}

func TestQuestionReplyLinkSkipsContinuationHeading(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "Check the audience"},
		{Seq: 2, Agent: "Main", Kind: "tool", Text: "Read `config.go`"},
		{Seq: 3, Agent: "/root/worker", Kind: "final", Text: "Checked"},
	}})
	questionTestAsync(t, u, "ask", "Which audience?")
	lifecycleUserMessage(t, u, "turn", "reply", lifecycleReply(t, "ask", 0, "Which audience?", "Customers"))
	var reply uint64
	for _, entry := range u.view.entries {
		if entry.Kind == "question_reply" {
			reply = entry.Seq
		}
	}
	feed := u.view.renderFeed(100, 60)
	row, ok := u.view.questionRows[reply]
	if !ok || row >= len(feed.lines) || !strings.Contains(feed.lines[row], "Asked") {
		t.Fatalf("answer target is not Asked: row=%d frame=%s", row, strings.Join(feed.lines, "\n"))
	}
}

func TestSyncQuestionReplyBecomesMainAndJournalAnchor(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	u.turn = "turn"
	lifecycleUserMessage(t, u, "turn", "prompt", "Original task")
	questionTestSync(t, u, "sync", false)
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "sync"})
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": appServerItem{ID: "ack", Type: "agentMessage", Text: "I will use that scope."}})
	frame := mainFeed(u, 100)
	if !strings.Contains(frame, "re: your answer") || strings.Contains(frame, "re: your message") {
		t.Fatalf("sync answer not used for reply:\n%s", frame)
	}
	u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "answer", Text: "Scope applied.", Question: "Original task"}, terminal: true, batch: 1})
	frame = mainFeed(u, 100)
	if strings.Count(frame, "re: your answer") != 2 || strings.Contains(frame, "re: your message") {
		t.Fatalf("sync answer not used for journal reply:\n%s", frame)
	}
}
