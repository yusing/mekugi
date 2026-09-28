package router

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

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
	}
}
