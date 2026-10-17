package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/backends/codex/appserver"
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
	if u.attached.session.KnownCaller(u.Thread) == "" {
		u.startSession(u.Thread, u.attached.cwd)
	}
	if terminalTest(u) == nil {

		t.Cleanup(func() { terminalTest(u).Diff.Close() })
	}
	if err := u.testPaint(io.Discard, width+2, 31); err != nil {
		t.Fatal(err)
	}
	return strings.Join(terminalTest(u).Frame.Rows, "\n")
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
			u.Turn = "turn"
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
	u.Turn = "turn"
	questionTestSync(t, u, "stale", false)
	questionTestPaint(t, u, 60)
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "stale"})
	if u.activeQuestionCall() != nil || u.questionCount() != 0 {
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

func TestNativeChildQuestionUsesBlockedFallback(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "main-turn"
	u.notifications = new(nativeNotifications)
	request := func(thread string) {
		questionTestMessage(t, u, thread+"-request", "item/tool/requestUserInput", map[string]any{
			"threadId": thread, "turnId": thread + "-turn", "itemId": "shared-item",
			"questions": []any{map[string]any{"id": "scope", "question": "Which scope?"}},
		})
	}
	request("child")
	frame := questionTestPaint(t, u, 140)
	if !u.attached.presentation.Status.Alert || !strings.Contains(frame, "Blocked on unsupported request item/tool/requestUserInput") || !strings.Contains(frame, "Ctrl-C interrupts") {
		t.Fatalf("child request was not visibly blocked: %s", frame)
	}
	blocked := u.notifications.blocked[`"child-request"`]
	if blocked.Thread != "child" || blocked.Turn != "child-turn" || len(u.notifications.blocked) != 1 {
		t.Fatalf("blocked identity = %+v", u.notifications.blocked)
	}
	if wire.Len() != 0 || u.questionCount() != 0 || u.activeQuestionCall() != nil || u.Thread != "main" {
		t.Fatalf("child request acquired Main input or received a response: %s", wire.String())
	}

	request("main")
	if u.questionCount() != 1 || u.activeQuestionCall() == nil || u.activeQuestionCall().Thread != "main" {
		t.Fatal("Main request with the same item/question identities was not answerable")
	}
	questionTestPaint(t, u, 140)
	appServerTestKeys(t, u, "Main answer\r")
	want := `{"id":"main-request","result":{"answers":{"scope":{"answers":["user_note: Main answer"]}}}}` + "\n"
	if wire.String() != want {
		t.Fatalf("response = %s, want %s", wire.String(), want)
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "main-request"})
	if len(u.notifications.blocked) != 1 {
		t.Fatal("Main response resolved the child request")
	}

	wire.Reset()
	appServerTestKeys(t, u, "\x03")
	var interrupt struct {
		Method string `json:"method"`
		Params struct {
			Thread string `json:"threadId"`
			Turn   string `json:"turnId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(wire.Bytes(), &interrupt); err != nil {
		t.Fatal(err)
	}
	if interrupt.Method != "turn/interrupt" || interrupt.Params.Thread != "main" || interrupt.Params.Turn != "main-turn" {
		t.Fatalf("Ctrl-C did not interrupt Main's waiting turn: %s", wire.String())
	}
}

func TestNativeQuestionAsyncBatchAndExternalCommit(t *testing.T) {
	u, input := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "async-item", "Who receives it?", "Who approves it?")
	if u.activeQuestionCall() == nil || u.questionCount() != 2 {
		t.Fatal("async question did not open or was lost")
	}
	if slices.ContainsFunc(u.attached.source.Entries, func(entry sessionpresentation.ActivityRecord) bool { return entry.Source.Kind == "final" }) {
		t.Fatal("async question entered final-answer bookkeeping")
	}
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r2\r")
	if strings.Count(input.String(), binding.QuestionReplyStart) != 1 {
		t.Fatalf("async answers were not batched: %q", input.String())
	}
	if !strings.Contains(input.String(), `request_user_input_async`) {
		t.Fatal("missing stock question identity")
	}
	// A second client may commit a reply without any local submission.
	v, _ := newAppServerTestUI()
	v.Turn = "turn"
	questionTestAsync(t, v, "external-item", "Which audience?")
	id, _ := json.Marshal([]any{"request_user_input_async", "external-item", 0})
	envelope, _ := json.Marshal([]binding.QuestionReply{{Answer: "Customers", Question: "Which audience?", QuestionItemID: string(id)}})
	questionTestMessage(t, v, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "external-reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": binding.QuestionReplyStart + string(envelope) + binding.QuestionReplyEnd}},
	}})
	if got := v.questionCalls()[0].Questions[0].Outcome; got != "answered elsewhere" {
		t.Fatalf("external outcome = %q", got)
	}
}

func TestNativeQuestionParkedEditorMidTypingAndSecret(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "turn"
	appServerTestKeys(t, u, "draft")
	questionTestSync(t, u, "midtyping", false)
	if u.activeQuestionCall() != nil {
		t.Fatal("sync question took focus mid-typing")
	}
	appServerTestKeys(t, u, "7")
	if u.Text != "draft7" {
		t.Fatalf("keystroke leaked into question: %q", u.Text)
	}
	before := u.Editor.Park()
	u.openQuestions()
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "3private")
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	if !reflect.DeepEqual(u.Editor.Park(), before) {
		t.Fatalf("parked draft not restored: %q", u.Text)
	}
	u.openQuestions()
	questionTestPaint(t, u, 60)
	if u.Text != "private" {
		t.Fatalf("answer draft lost on reopen: %q", u.Text)
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "midtyping"})
	if len(u.InputHistory) == 0 || u.InputHistory[len(u.InputHistory)-1].Text != "private" {
		t.Fatal("unsent answer not saved to input history")
	}
	v, _ := newAppServerTestUI()
	v.Turn = "turn"
	questionTestSync(t, v, "secret", true)
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "3secret")
	questionTestMessage(t, v, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "secret"})
	if len(v.InputHistory) != 0 {
		t.Fatal("secret answer entered input history")
	}
}

func TestNativeQuestionExternalReplyParksActiveAnswerBeforeCommit(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(fmt.Sprintf("secret=%v", secret), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			u.Turn = "turn"
			u.insertDraft("Main draft")
			questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
				"id": "call", "type": "agentMessage", "delivery": "async", "questions": []any{
					map[string]any{"question": "First?", "isSecret": secret}, map[string]any{"question": "Second?"},
				},
			}})
			u.openQuestions()
			u.insertDraft("active answer")
			commit := func(index int) {
				text := binding.EncodeQuestionReplies([]binding.QuestionReply{{Answer: "external", QuestionItemID: binding.AsyncQuestionIdentity("call", index)}})
				questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
					"id": fmt.Sprintf("reply-%d", index), "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text}},
				}})
			}
			commit(1) // Another question in the active call commits first.
			if u.activeQuestionCall() != nil || u.Text != "Main draft" || len(u.InputHistory) != 0 {
				t.Fatal("external reply did not restore Main before commitment")
			}
			u.openQuestions()
			if u.Text != "active answer" {
				t.Fatal("reply to another question lost the active answer")
			}
			commit(0)
			commit(0) // Duplicate delivery cannot remember or revive the draft.
			u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
			u.openQuestions()
			if u.activeQuestionCall() != nil || u.Text != "Main draft" || !u.questionCalls()[0].Resolved {
				t.Fatal("resolved answer was revived or replaced Main")
			}
			if secret {
				if len(u.InputHistory) != 0 || u.attached.conversation.Editor.Text != "Main draft" {
					t.Fatal("secret entered ordinary editor/history")
				}
			} else if len(u.InputHistory) != 1 || u.InputHistory[0].Text != "active answer" {
				t.Fatal("discarded ordinary answer was not saved exactly once")
			}
		})
	}
}

func TestNativeQuestionsUseComposedAttachment(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertDraft("pre-start draft")
	editor := u.Editor
	questions := &u.attached.conversation.Questions
	questionTestSync(t, u, "pending", false)
	if u.questions.Questions != questions || questions.Count("main") != 1 {
		t.Fatal("native question did not reach the composed owner")
	}
	u.startSession("main", "/workspace")

	if &u.attached.conversation.Questions != questions || u.questionCount() != 1 || editor != &u.attached.conversation.Editor || u.Text != "pre-start draft" {
		t.Fatal("first host identity replaced pending question state")
	}
	replaceAppServerTestSession(t, u, "replacement")
	u.Thread = "replacement"
	if u.Editor == editor || u.Editor != &u.attached.conversation.Editor || u.Text != "pre-start draft" || editor.Text != "" {
		t.Fatal("replacement did not transfer editable input to its new owner")
	}
	if u.questions.Questions == questions || u.questions.Questions != &u.attached.conversation.Questions || u.questionCount() != 0 {
		t.Fatal("replacement retained the retired question owner")
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "pending"})
	if len(u.questions.Calls) != 0 {
		t.Fatal("retired response populated replacement questions")
	}
}

func TestNativeQuestionSupersedeBannerAndRejectedSend(t *testing.T) {
	t.Run("rejected send", func(t *testing.T) {
		u, input := newAppServerTestUI()
		u.Turn = "turn"
		u.Text = "kept main draft"
		questionTestAsync(t, u, "pending", "Who receives it?")
		u.openQuestions()
		questionTestPaint(t, u, 70)
		appServerTestKeys(t, u, "3Customers only\r")
		if !strings.Contains(input.String(), "turn/steer") {
			t.Fatalf("question was not sent: %q", input.String())
		}
		appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
		if u.questionCount() != 1 || u.questionCalls()[0].Sent {
			t.Fatal("rejected question did not reopen")
		}
		if u.Text != "kept main draft" {
			t.Fatalf("main draft changed: %q", u.Text)
		}
		u.openQuestions()
		questionTestPaint(t, u, 70)
		if u.Text != "Customers only" {
			t.Fatalf("answer draft changed: %q", u.Text)
		}
	})
	t.Run("supersede and steer", func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.Text = "new prompt"
		questionTestAsync(t, u, "pending", "Who receives it?")
		if got := strings.Join(testQuestionRows(u, 80, 10), " "); !strings.Contains(got, "dismisses 1 questions") {
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
		if got := u.questionCalls()[0].Questions[0].Outcome; got != "superseded" {
			t.Fatalf("new-turn outcome = %q", got)
		}
	})
}

func TestNativeQuestionResumeOnlyUnanswered(t *testing.T) {
	u, _ := newAppServerTestUI()

	u.startSession("main", t.TempDir())
	id, err := json.Marshal([]any{"request_user_input_async", "answered-item", 0})
	if err != nil {
		t.Fatal(err)
	}
	replies, err := json.Marshal([]binding.QuestionReply{{Answer: "Customers", Question: "Who receives it?", QuestionItemID: string(id)}})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal([]any{map[string]any{"id": "past-turn", "status": "completed", "items": []any{
		map[string]any{"id": "answered-item", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Who receives it?", "questions": []any{map[string]any{"title": "Who receives it?", "options": []string{"Customers", "Internal"}}}},
		map[string]any{"id": "reply", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": binding.QuestionReplyStart + string(replies) + binding.QuestionReplyEnd}}},
		map[string]any{"id": "open-item", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Who approves it?", "questions": []any{map[string]any{"title": "Who approves it?", "options": []string{"Customers", "Internal"}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var turns []binding.HistoryTurn
	if err := json.Unmarshal(wire, &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	if u.questionCount() != 1 || len(u.questionCalls()) != 2 {
		t.Fatalf("resume questions: pending=%d calls=%d", u.questionCount(), len(u.questionCalls()))
	}
	if got := u.questionCalls()[0].Questions[0].Outcome; got != "answered elsewhere" {
		t.Fatalf("answered replay outcome = %q", got)
	}
	if u.activeQuestionCall() != nil {
		t.Fatal("replay stole focus")
	}
}

func TestNativeQuestionGapsQueueAndLiteralAnswers(t *testing.T) {
	u, w := newAppServerTestUI()
	u.Turn = "turn"
	questionTestAsync(t, u, "gaps", "First?", "Last?")
	u.openQuestions()
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "\x1b[C1\r")
	if !u.questions.Confirm || w.Len() != 0 {
		t.Fatal("last question silently submitted gaps")
	}
	appServerTestKeys(t, u, "\r")
	if strings.Count(w.String(), binding.QuestionReplyStart) != 1 || strings.Contains(w.String(), `\"question\":\"First?\"`) {
		t.Fatalf("gap reply: %s", w.String())
	}

	v, wire := newAppServerTestUI()
	v.Turn = "turn"
	appServerTestKeys(t, v, "main draft")
	questionTestSync(t, v, "queue", false)
	appServerTestKeys(t, v, "\r")
	if len(v.attached.conversation.Queued) != 1 || wire.Len() != 0 {
		t.Fatal("hidden sync steered instead of queueing")
	}
	v.openQuestions()
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "\x1b[200~! @file $skill /quit\x1b[201~")
	if v.attached.conversation.ShellMode() || v.picker.Open || len(v.Files) != 0 || v.Text != "! @file $skill /quit" {
		t.Fatal("answer paste was interpreted")
	}
}

func TestNativeQuestionBeforePaintAndSecretFrame(t *testing.T) {
	u, w := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "unpainted", false)
	appServerTestKeys(t, u, "7")
	if u.Text != "7" || u.activeQuestionCall() != nil || w.Len() != 0 {
		t.Fatal("buffered key reached unseen dock")
	}
	v, _ := newAppServerTestUI()
	v.Turn = "turn"
	questionTestSync(t, v, "secret-frame", true)
	questionTestPaint(t, v, 60)
	appServerTestKeys(t, v, "3SECRET-ANSWER")
	if frame := questionTestPaint(t, v, 60); strings.Contains(frame, "SECRET") || !strings.Contains(frame, "•") {
		t.Fatal("secret editor is not masked")
	}
}

func TestNativeQuestionsOpenNextCallWithoutShortcut(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "turn"
	questionTestAsync(t, u, "first", "First question?")
	questionTestAsync(t, u, "second", "Second question?")
	if u.activeQuestionCall() == nil || u.activeQuestionCall().Item != "first" {
		t.Fatal("first question did not open directly")
	}
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	if u.activeQuestionCall() == nil || u.activeQuestionCall().Item != "second" {
		t.Fatal("next call needs a shortcut")
	}
	if u.shell.Prompts.Ready() {
		t.Fatal("new question accepts an answer before being painted")
	}
	if got := questionTestPaint(t, u, 70); !strings.Contains(got, "Second question?") {
		t.Fatalf("next question not visible:\n%s", got)
	}
}

func TestNativeQuestionsWaitForEmptyComposerAndRespectHide(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "turn"
	appServerTestKeys(t, u, "draft")
	questionTestAsync(t, u, "pending", "Pending question?")
	questionTestPaint(t, u, 70)
	if u.activeQuestionCall() != nil || u.Text != "draft" {
		t.Fatal("arrival disrupted a nonempty composer")
	}
	appServerTestKeys(t, u, "\x7f\x7f\x7f\x7f\x7f")
	questionTestPaint(t, u, 70)
	if u.activeQuestionCall() == nil {
		t.Fatal("empty composer did not show question")
	}
	// Escape is assembled by the input loop; exercise its completed key.
	u.questionKey("\x1b")
	questionTestPaint(t, u, 70)
	if u.activeQuestionCall() != nil {
		t.Fatal("paint undid explicit hide")
	}
	u.openQuestions()
	if u.activeQuestionCall() == nil {
		t.Fatal("manual reopening failed")
	}
}

func TestNativeSingleQuestionOmitsNavigationHints(t *testing.T) {
	for _, width := range []int{24, 72, 160} {
		for _, titles := range [][]string{{"Only question?"}, {"First question?", "Second question?"}} {
			u, _ := newAppServerTestUI()
			questionTestAsync(t, u, "hints", titles...)
			for _, height := range []int{8, 20} {
				rows := strings.Join(testQuestionRows(u, width, height), "\n")
				hasNext := strings.Contains(rows, "next")
				hasArrows := strings.Contains(rows, "←") || strings.Contains(rows, "→")
				if want := len(titles) > 1; hasNext != want || hasArrows != want {
					t.Fatalf("%d questions at %dx%d have wrong hints:\n%s", len(titles), width, height, rows)
				}
			}
		}
	}
}

func TestNativeQuestionAnswerWordNavigation(t *testing.T) {
	for _, mapping := range []struct{ name, left, right string }{
		{"alt", "\x1b[1;3D", "\x1b[1;3C"},
		{"ctrl", "\x1b[1;5D", "\x1b[1;5C"},
		{"option", "\x1bb", "\x1bf"},
	} {
		for _, mode := range []string{"async", "sync", "note"} {
			t.Run(mapping.name+"/"+mode, func(t *testing.T) {
				u, w := newAppServerTestUI()
				if mode == "async" {
					questionTestAsync(t, u, "words", "First?", "Second?")
				} else {
					questionTestSync(t, u, "words", false)
				}
				questionTestPaint(t, u, 80)
				shell := terminalTest(u)
				send := func(keys string) {
					t.Helper()
					for _, key := range []byte(keys) {
						if err := shell.key(key); err != nil {
							t.Fatal(err)
						}
					}
				}
				if mode == "note" {
					send("\t")
				}
				send("one two three" + mapping.left + mapping.left + "X" + mapping.right + "Y")
				if u.Text != "one XtwoY three" || u.questions.Index != 0 || w.Len() != 0 {
					t.Fatalf("draft=%q question=%d requests=%s", u.Text, u.questions.Index, w.String())
				}
				if mode == "note" && !u.currentQuestion().Note {
					t.Fatal("word movement left the option note")
				}
				if mode == "async" {
					// Choosing an option leaves answer editing, so bare arrows
					// can still navigate questions without losing the draft.
					send("\x10")
					send("\x1b[C")
					if u.questions.Index != 1 || u.Text != "" {
						t.Fatal("plain right did not navigate to the second question")
					}
					send("\x1b[D")
					if u.questions.Index != 0 || u.Text != "one XtwoY three" {
						t.Fatal("plain left did not restore the first answer")
					}
				}
			})
		}
	}
}

func TestNativeQuestionAnswerBareArrowNavigation(t *testing.T) {
	for _, mode := range []string{"async", "sync", "note"} {
		t.Run(mode, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			if mode == "async" {
				questionTestAsync(t, u, "arrows", "First?", "Second?")
			} else {
				questionTestSync(t, u, "arrows", false)
			}
			questionTestPaint(t, u, 80)

			send := func(keys string) {
				t.Helper()
				for _, key := range []byte(keys) {
					if err := terminalTest(u).key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "note" {
				send("\t")
			}
			send("one two\x1b[D\x1b[DX\x1b[CY")
			if u.Text != "one tXwYo" || u.questions.Index != 0 || wire.Len() != 0 {
				t.Fatalf("draft=%q question=%d requests=%s", u.Text, u.questions.Index, wire.String())
			}
			if mode == "note" && !u.currentQuestion().Note {
				t.Fatal("caret movement left the option note")
			}
		})
	}
}

func TestUISnapshotQuestionArrowHints(t *testing.T) {
	for _, width := range []int{28, 80} {
		for _, editing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", width, editing), func(t *testing.T) {
				u, _ := newAppServerTestUI()
				questionTestAsync(t, u, "arrows", "First?", "Second?")
				questionTestPaint(t, u, width)
				if editing {
					appServerTestKeys(t, u, "answer")
				}
				assertNativeUISnapshot(t, fmt.Sprintf("question-arrow-hints-%d-%t", width, editing), testQuestionRows(u, width, 10))
			})
		}
	}
}

func testQuestionRows(u *appServerUI, width, height int) []string {
	return conversationui.Questions(u.questions.View(u.Thread), &u.shell.Transcript.Painter.Painter, u.shell.Prompts.QuestionLayout(u.questions.View(u.Thread)), terminalsession.SelectionStyle(&u.shell.Transcript.Painter.Painter), u.Turn == "" && u.Text != "", width, height)
}
