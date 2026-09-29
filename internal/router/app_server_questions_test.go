package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/appserver"
)

func questionTestMessage(t *testing.T, u *appServerUI, id any, method string, params any) {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	m := appserver.Message{Method: method, Params: jsontext.Value(data)}
	if id != nil {
		raw, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = jsontext.Value(raw)
	}
	if err := u.message(m); err != nil {
		t.Fatal(err)
	}
}

func questionTestSync(t *testing.T, u *appServerUI, id string, secret bool) {
	t.Helper()
	questionTestMessage(t, u, id, "item/tool/requestUserInput", map[string]any{
		"threadId": "main", "turnId": "turn", "itemId": "sync-" + id, "isBlocking": false,
		"questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Which release scope?", "isOther": true, "isSecret": secret,
			"options": []any{map[string]any{"label": "Narrow", "description": "Only one path"}, map[string]any{"label": "Broad", "description": "All paths"}},
		}},
	})
}

func questionTestAsync(t *testing.T, u *appServerUI, item string, titles ...string) {
	t.Helper()
	questions := make([]any, 0, len(titles))
	for _, title := range titles {
		questions = append(questions, map[string]any{"title": title, "options": []string{"Customers", "Internal"}})
	}
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": item, "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": titles[0], "questions": questions,
	}})
}

func questionTestPaint(t *testing.T, u *appServerUI, width int) string {
	t.Helper()
	rows, _ := u.mainFrame(width, 28, 0)
	return strings.Join(rows, "\n")
}

func TestNativeQuestionSyncAnswerEncoding(t *testing.T) {
	for _, tc := range []struct{ name, keys, want string }{
		{"option", "1\r", `"Narrow"`},
		{"note", "1\tbecause tests\r", `"user_note: because tests"`},
		{"other", "3custom scope\r", `"user_note: custom scope"`},
		{"all skipped", "\x1d\r", `"answers":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, input := newAppServerTestUI()
			u.turn = "turn"
			questionTestSync(t, u, "sync-request", false)
			if !strings.Contains(questionTestPaint(t, u, 24), "Which release scope?") {
				t.Fatal("narrow dock lost question text")
			}
			appServerTestKeys(t, u, tc.keys)
			if !strings.Contains(input.String(), tc.want) {
				t.Fatalf("response %q lacks %q", input.String(), tc.want)
			}
		})
	}
}

func TestNativeQuestionResolvedAndInterrupt(t *testing.T) {
	u, input := newAppServerTestUI()
	u.turn = "turn"
	questionTestSync(t, u, "stale", false)
	questionTestPaint(t, u, 60)
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "stale"})
	if u.questions.active != nil || u.questionCount() != 0 {
		t.Fatal("resolved request remains open")
	}
	appServerTestKeys(t, u, "1\r")
	if strings.Contains(input.String(), `"answers"`) {
		t.Fatal("stale question was answered")
	}
	questionTestSync(t, u, "interrupt", false)
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "\x03")
	if !strings.Contains(input.String(), "turn/interrupt") {
		t.Fatal("Ctrl-C did not interrupt waiting turn")
	}
}

func TestNativeQuestionAsyncBatchAndExternalCommit(t *testing.T) {
	u, input := newAppServerTestUI()
	u.session.start("main", t.TempDir())
	u.turn = "turn"
	questionTestAsync(t, u, "async-item", "Who receives it?", "Who approves it?")
	if u.questions.active == nil || u.questionCount() != 2 {
		t.Fatal("async question did not open or was lost")
	}
	if u.session.finals["main"] {
		t.Fatal("async question entered final-answer bookkeeping")
	}
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r2\r")
	if strings.Count(input.String(), questionReplyStart) != 1 {
		t.Fatalf("async answers were not batched: %q", input.String())
	}
	if !strings.Contains(input.String(), `request_user_input_async`) {
		t.Fatal("missing stock question identity")
	}
	// A second client may commit a reply without any local submission.
	v, _ := newAppServerTestUI()
	v.turn = "turn"
	questionTestAsync(t, v, "external-item", "Which audience?")
	id, _ := json.Marshal([]any{"request_user_input_async", "external-item", 0})
	envelope, _ := json.Marshal([]nativeQuestionReply{{Answer: "Customers", Question: "Which audience?", QuestionItemID: string(id)}})
	questionTestMessage(t, v, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "external-reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": questionReplyStart + string(envelope) + questionReplyEnd}},
	}})
	if got := v.questions.calls[0].questions[0].outcome; got != "answered elsewhere" {
		t.Fatalf("external outcome = %q", got)
	}
}

func TestNativeQuestionParkedEditorMidTypingAndSecret(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn = "turn"
	appServerTestKeys(t, u, "draft")
	questionTestSync(t, u, "midtyping", false)
	if u.questions.active != nil {
		t.Fatal("sync question took focus mid-typing")
	}
	appServerTestKeys(t, u, "7")
	if u.draft != "draft7" {
		t.Fatalf("keystroke leaked into question: %q", u.draft)
	}
	before := u.saveQuestionEditor()
	u.openQuestions()
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "3private")
	u.hideQuestions()
	if !reflect.DeepEqual(u.saveQuestionEditor(), before) {
		t.Fatalf("parked draft not restored: %q", u.draft)
	}
	u.openQuestions()
	questionTestPaint(t, u, 60)
	if u.draft != "private" {
		t.Fatalf("answer draft lost on reopen: %q", u.draft)
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "midtyping"})
	if len(u.inputHistory) == 0 || u.inputHistory[len(u.inputHistory)-1].text != "private" {
		t.Fatal("unsent answer not saved to input history")
	}
	v, _ := newAppServerTestUI()
	v.turn = "turn"
	questionTestSync(t, v, "secret", true)
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "3secret")
	questionTestMessage(t, v, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "secret"})
	if len(v.inputHistory) != 0 {
		t.Fatal("secret answer entered input history")
	}
}

func TestNativeQuestionSupersedeBannerAndRejectedSend(t *testing.T) {
	t.Run("rejected send", func(t *testing.T) {
		u, input := newAppServerTestUI()
		u.turn = "turn"
		u.draft = "kept main draft"
		questionTestAsync(t, u, "pending", "Who receives it?")
		u.openQuestions()
		questionTestPaint(t, u, 70)
		appServerTestKeys(t, u, "3Customers only\r")
		if !strings.Contains(input.String(), "turn/steer") {
			t.Fatalf("question was not sent: %q", input.String())
		}
		appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
		if u.questionCount() != 1 || u.questions.calls[0].sent {
			t.Fatal("rejected question did not reopen")
		}
		if u.draft != "kept main draft" {
			t.Fatalf("main draft changed: %q", u.draft)
		}
		u.openQuestions()
		questionTestPaint(t, u, 70)
		if u.draft != "Customers only" {
			t.Fatalf("answer draft changed: %q", u.draft)
		}
	})
	t.Run("supersede and steer", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.draft = "new prompt"
		questionTestAsync(t, u, "pending", "Who receives it?")
		if got := strings.Join(u.questionRows(80, 10), " "); !strings.Contains(got, "dismisses 1 questions") {
			t.Fatalf("missing supersede warning: %q", got)
		}
		questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
			"id": "steer", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "a same-turn steer"}},
		}})
		if u.questionCount() != 1 {
			t.Fatal("same-turn steer superseded question")
		}
		questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "next-turn", "item": map[string]any{
			"id": "new-prompt", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "new prompt"}},
		}})
		if got := u.questions.calls[0].questions[0].outcome; got != "superseded" {
			t.Fatalf("new-turn outcome = %q", got)
		}
	})
}

func TestNativeQuestionResumeOnlyUnanswered(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.agents = newLiveActivityView()
	u.session.start("main", t.TempDir())
	id, err := json.Marshal([]any{"request_user_input_async", "answered-item", 0})
	if err != nil {
		t.Fatal(err)
	}
	replies, err := json.Marshal([]nativeQuestionReply{{Answer: "Customers", Question: "Who receives it?", QuestionItemID: string(id)}})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal([]any{map[string]any{"id": "past-turn", "status": "completed", "items": []any{
		map[string]any{"id": "answered-item", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Who receives it?", "questions": []any{map[string]any{"title": "Who receives it?", "options": []string{"Customers", "Internal"}}}},
		map[string]any{"id": "reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": questionReplyStart + string(replies) + questionReplyEnd}}},
		map[string]any{"id": "open-item", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Who approves it?", "questions": []any{map[string]any{"title": "Who approves it?", "options": []string{"Customers", "Internal"}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var turns []appServerHistoryTurn
	if err := json.Unmarshal(wire, &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	if u.questionCount() != 1 || len(u.questions.calls) != 2 {
		t.Fatalf("resume questions: pending=%d calls=%d", u.questionCount(), len(u.questions.calls))
	}
	if got := u.questions.calls[0].questions[0].outcome; got != "answered elsewhere" {
		t.Fatalf("answered replay outcome = %q", got)
	}
	if u.questions.active != nil {
		t.Fatal("replay stole focus")
	}
}

func TestNativeQuestionGapsQueueAndLiteralAnswers(t *testing.T) {
	u, w := newAppServerTestUI()
	u.turn = "turn"
	questionTestAsync(t, u, "gaps", "First?", "Last?")
	u.openQuestions()
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "\x1b[C1\r")
	if !u.questions.confirm || w.Len() != 0 {
		t.Fatal("last question silently submitted gaps")
	}
	appServerTestKeys(t, u, "\r")
	if strings.Count(w.String(), questionReplyStart) != 1 || strings.Contains(w.String(), `\"question\":\"First?\"`) {
		t.Fatalf("gap reply: %s", w.String())
	}

	v, wire := newAppServerTestUI()
	v.turn = "turn"
	appServerTestKeys(t, v, "main draft")
	questionTestSync(t, v, "queue", false)
	appServerTestKeys(t, v, "\r")
	if len(v.queued) != 1 || wire.Len() != 0 {
		t.Fatal("hidden sync steered instead of queueing")
	}
	v.openQuestions()
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "\x1b[200~! @file $skill /quit\x1b[201~")
	if v.shellMode() || v.picker.open || len(v.files) != 0 || v.draft != "! @file $skill /quit" {
		t.Fatal("answer paste was interpreted")
	}
	appServerTestKeys(t, v, "\x16")
	if v.notice != "answers are text only" {
		t.Fatal("image paste was not rejected")
	}
}

func TestNativeQuestionBeforePaintAndSecretFrame(t *testing.T) {
	u, w := newAppServerTestUI()
	u.turn = "turn"
	questionTestSync(t, u, "unpainted", false)
	appServerTestKeys(t, u, "7")
	if u.draft != "7" || u.questions.active != nil || w.Len() != 0 {
		t.Fatal("buffered key reached unseen dock")
	}
	v, _ := newAppServerTestUI()
	v.turn = "turn"
	questionTestSync(t, v, "secret-frame", true)
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "3SECRET-ANSWER")
	if frame := questionTestPaint(t, v, 60); strings.Contains(frame, "SECRET") || !strings.Contains(frame, "•") {
		t.Fatal("secret editor is not masked")
	}
}

func TestNativeQuestionsOpenNextCallWithoutShortcut(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn = "turn"
	questionTestAsync(t, u, "first", "First question?")
	questionTestAsync(t, u, "second", "Second question?")
	if u.questions.active == nil || u.questions.active.item != "first" {
		t.Fatal("first question did not open directly")
	}
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	if u.questions.active == nil || u.questions.active.item != "second" {
		t.Fatal("next call needs a shortcut")
	}
	if u.questions.painted {
		t.Fatal("new question accepts an answer before being painted")
	}
	if got := questionTestPaint(t, u, 70); !strings.Contains(got, "Second question?") {
		t.Fatalf("next question not visible:\n%s", got)
	}
}

func TestNativeQuestionsWaitForEmptyComposerAndRespectHide(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn = "turn"
	appServerTestKeys(t, u, "draft")
	questionTestAsync(t, u, "pending", "Pending question?")
	questionTestPaint(t, u, 70)
	if u.questions.active != nil || u.draft != "draft" {
		t.Fatal("arrival disrupted a nonempty composer")
	}
	appServerTestKeys(t, u, "\x7f\x7f\x7f\x7f\x7f")
	questionTestPaint(t, u, 70)
	if u.questions.active == nil {
		t.Fatal("empty composer did not show question")
	}
	// Escape is assembled by the input loop; exercise its completed key.
	u.questionKey("\x1b")
	questionTestPaint(t, u, 70)
	if u.questions.active != nil {
		t.Fatal("paint undid explicit hide")
	}
	u.openQuestions()
	if u.questions.active == nil {
		t.Fatal("manual reopening failed")
	}
}
