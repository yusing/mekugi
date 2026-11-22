package launch

import (
	json "encoding/json/v2"
	"fmt"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestAsyncPartialAnswersByRuntimeID(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser", true: "native"}[native], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			u.Turn = "turn"
			item := binding.ThreadItem{ID: "partial", Type: "agentMessage", Delivery: "async", Questions: []binding.Question{
				{ID: "q1", Question: "First?"}, {ID: "q2", Question: "Second?", Options: []binding.QuestionOption{{Label: "A"}, {Label: "B", Preview: "details", Recommended: true}}},
			}}
			questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			call := u.questions.Calls[0]
			if call.Questions[1].Selected != 1 || call.Questions[1].Done {
				t.Fatal("recommended option auto-submitted or not selected")
			}
			submit := func(id string, choice *int, other, note string) {
				t.Helper()
				if native {
					index := 0
					if id == "q2" {
						index = 1
					}
					selected := len(call.Questions[index].Choices)
					if choice != nil {
						selected = *choice
					}
					input := sessionapi.NativeAnswers{Call: call.ID, Answers: []sessionapi.NativeQuestionAnswer{{Call: call.ID, Question: id, Choice: selected, Draft: sessionapi.QuestionAnswer{Text: other}.Draft(), Note: note}}}
					if refusal := nativeTestAnswers(t, u, input); refusal != "" {
						t.Fatal(refusal)
					}
				} else if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.AnswerQuestions, Answer: sessionapi.Answer{Call: call.ID, Answers: []sessionapi.QuestionAnswer{{Question: id, Choice: choice, Text: other, Note: note}}}}); refusal != "" {
					t.Fatal(refusal)
				}
			}
			submit("q2", new(1), "", "because")
			requests := appServerTurnRequests(t, wire)
			if len(requests) != 1 {
				t.Fatal(requests)
			}
			first := requests[0].text()
			if first != `<question_answers>[{"id":"q2","choice":2,"note":"because"}]</question_answers>` {
				t.Fatal(first)
			}
			if call.Sent || call.Resolved || !call.Questions[1].Sent || call.Questions[0].Done || !call.View().Questions[0].Pending {
				t.Fatalf("partial answer closed sibling: %+v", call)
			}
			if u.questions.Count("main") != 1 {
				t.Fatal("unanswered question not navigable")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"turn"}}`, requests[0].ID))
			lifecycleUserMessage(t, u, "answer-turn", "reply-q2", first)
			if call.Questions[1].Outcome != "answered" || call.Questions[0].Outcome != "" {
				t.Fatalf("wrong commitment: %+v", call)
			}
			wire.Reset()
			submit("q1", nil, "written answer", "")
			requests = appServerTurnRequests(t, wire)
			if len(requests) != 1 {
				t.Fatal(requests)
			}
			second := requests[0].text()
			if second != `<question_answers>[{"id":"q1","other":"written answer"}]</question_answers>` {
				t.Fatal(second)
			}
			lifecycleUserMessage(t, u, "answer-turn-2", "reply-q1", second)
			if !call.Resolved {
				t.Fatal("all committed answers left call open")
			}
			for _, replay := range []bool{false, true} {
				v := u
				if replay {
					v, _ = newAppServerTestUI()
					v.startSession("main", t.TempDir())
					content := func(text string) []byte {
						data, err := json.Marshal(conversationbinding.Input(sessionapi.QuestionAnswer{Text: text}.Draft()))
						if err != nil {
							t.Fatal(err)
						}
						return data
					}
					v.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}, {ID: "answer-turn", Status: "completed", Items: []binding.ThreadItem{{ID: "reply-q2", Type: "userMessage", Content: content(first)}}}})
					if v.questions.Count("main") != 1 || !v.questions.Calls[0].View().Questions[0].Pending {
						t.Fatal("restored partial reply closed q1")
					}
				}
				found := false
				for _, entry := range v.attached.source.Entries {
					if entry.Source.Kind == "question_reply" && entry.Source.Native.Item == "reply-q2" {
						found = entry.Source.Text == "B\nbecause" && entry.Source.Native.Question != 0
					}
				}
				if !found {
					t.Fatalf("readable activity reply missing (replay=%v)", replay)
				}
			}
		})
	}
}

func TestAsyncSubsetRejectsOnlyItsQuestions(t *testing.T) {
	call := &prompt.Call{ID: "call", Questions: []prompt.Question{{ID: "q1", Done: true}, {ID: "q2", Done: true}}}
	call.SubmitIDs = []string{"q2"}
	call.Submitted()
	call.SetSubmission("second", []string{"q2"})
	call.SubmitIDs = []string{"q1"}
	call.Submitted()
	call.SetSubmission("first", []string{"q1"})
	call.RejectQuestions([]string{"q2"})
	if !call.Questions[0].Sent || call.Questions[0].SubmissionID != "first" || call.Questions[1].Sent || call.Sent {
		t.Fatalf("rejection affected a different submission: %+v", call)
	}
}

func TestAsyncSkipAndBlockingAdmission(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		call := &prompt.Call{ID: "call", Sync: blocking, Questions: []prompt.Question{{ID: "q1"}, {ID: "q2"}}}
		answers, incomplete, refusal := prepareCallAnswers(call, sessionapi.Answer{Call: "call", Answers: []sessionapi.QuestionAnswer{{Question: "q2", Skip: true}}})
		if blocking {
			if refusal == "" {
				t.Fatal("blocking subset admitted")
			}
			continue
		}
		if refusal != "" || !incomplete || len(answers) != 1 {
			t.Fatalf("skip admission: %+v %v %s", answers, incomplete, refusal)
		}
		questions := prompt.Questions{Calls: []*prompt.Call{call}}
		if _, err := questions.Answer(answers[0]); err != nil {
			t.Fatal(err)
		}
		if got := binding.QuestionReplies(conversationbinding.QuestionReplies(call)); !reflect.DeepEqual(got, []binding.QuestionAnswer{{ID: "q2", Skipped: true}}) {
			t.Fatal(got)
		}
	}
}
