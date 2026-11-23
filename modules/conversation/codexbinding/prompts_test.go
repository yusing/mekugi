package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// A non-terminal consumer uses the same decoded records, editor transitions,
// submission encoding, and committed facts as the live and restored adapters.
func TestQuestionLifecycleWithoutRenderer(t *testing.T) {
	var questions prompt.Questions
	var main composer.Editor
	main.InsertText("parked main")
	var editor prompt.Editor
	call := Questions("main", "turn", "ask", nil, []binding.Question{{ID: "q1", Question: "Audience?", Options: []binding.QuestionOption{{Label: "Developers"}}}, {ID: "q2", Question: "Why?"}})
	if !questions.Add(&call, true) || questions.ShouldOpen(true) {
		t.Fatal("restoration took focus")
	}
	if questions.Add(&call, false) {
		t.Fatal("duplicate call admitted")
	}
	if !questions.Open(questions.Next()) {
		t.Fatal("could not open restored question")
	}
	editor.Open(&main, questions.Current().Editor)
	q := questions.Current()
	q.Complete("", false, false)
	if step, submit := questions.Advance(); step != 1 || submit {
		t.Fatal("did not advance to unanswered question")
	}
	questions.Move(1)
	main.InsertText("user_note: literal")
	q = questions.Current()
	q.Editor = main.Park()
	q.Complete(main.Text, false, false)
	reply := QuestionReplies(&call)
	editor.Close(&main)
	questions.Hide()
	call.Submitted()
	if main.Text != "parked main" || questions.Count("main") != 0 || call.Resolved {
		t.Fatal("submission confused editor ownership or host commitment")
	}
	if !call.Reject() || questions.Count("main") != 2 || call.Questions[1].Editor.Snapshot.Text != "user_note: literal" {
		t.Fatal("rejection lost answer draft")
	}
	call.Submitted()
	call.SetSubmission("sent", ReplyIDs(reply))
	_, targets, _ := ReplyText(reply)
	for i, r := range targets {
		if r.ID != call.Questions[i].ID {
			t.Fatal("unstable runtime identity")
		}
		answer, _ := r.Readable(call.Questions[i])
		call.Commit(i, "sent", answer)
	}
	view := call.View()
	if !view.Resolved || view.Questions[1].Answer[0] != "user_note: literal" || view.Questions[0].ID == view.Questions[1].ID {
		t.Fatal("committed view lost literal text or identity")
	}
	view.Questions[0].Choices[0].Label = "changed"
	view.Questions[0].Answer[0] = "changed"
	if call.Questions[0].Choices[0].Label != "Developers" || call.Questions[0].Answer[0] != "Developers" {
		t.Fatal("view aliases model state")
	}
}

func TestSynchronousAnswerEncodingKeepsModelNeutral(t *testing.T) {
	raw := jsontext.Value(`{"threadId":"main","turnId":"turn","itemId":"call","questions":[{"id":"q","question":"Pick?","options":[{"label":"Yes"}]},{"id":"secret","question":"Password?","isSecret":true}]}`)
	request, err := binding.DecodeQuestionRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	call := Questions(request.ThreadID, request.TurnID, request.ItemID, jsontext.Value(`"rpc"`), request.Questions)
	call.Questions[0].Complete("user_note: literal", true, false)
	call.Questions[1].Done = true
	response := QuestionResponse(&call, map[int][]string{1: {"secret bytes"}})
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var got any
	_ = json.Unmarshal(data, &got)
	var want any
	_ = json.Unmarshal([]byte(`{"answers":[{"id":"q","choice":1,"note":"user_note: literal"},{"id":"secret","other":"secret bytes"}]}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire response: %s", data)
	}
	call.Submitted()
	call.Resolve("answered")
	if call.Questions[0].Answer[1] != "user_note: literal" || len(call.View().Questions[1].Answer) != 0 || call.Questions[1].Editor.Snapshot.Text != "" {
		t.Fatal("wire encoding or secrets entered neutral view")
	}
}

func TestBlockingCompactAnswersAndRecommendation(t *testing.T) {
	call := Questions("main", "turn", "ask", jsontext.Value(`1`), []binding.Question{
		{ID: "q1", Question: "Pick?", Options: []binding.QuestionOption{{Label: "A"}, {Label: "B", Description: "detail", Preview: "a\nb", Recommended: true}}},
		{ID: "q2", Question: "Why?"}, {ID: "q3", Question: "Skip?"},
	})
	q := &call.Questions[0]
	if q.Selected != 1 || q.Done || q.Choices[0].Label != "A" || !q.Choices[1].Recommended || q.Choices[1].Preview != "a\nb" {
		t.Fatalf("recommendation reordered or submitted: %+v", q)
	}
	q.Complete("explanation", true, false)
	call.Questions[1].Complete("free text", true, false)
	call.Questions[2].Complete("", true, true)
	data, err := json.Marshal(QuestionResponse(&call, nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"answers":[{"id":"q1","choice":2,"note":"explanation"},{"id":"q2","other":"free text"},{"id":"q3","skipped":true}]}` {
		t.Fatal(string(data))
	}
}

func TestAsyncExplicitSkip(t *testing.T) {
	call := Questions("main", "turn", "ask", nil, []binding.Question{{ID: "q1", Question: "First?"}, {ID: "q2", Question: "Second?"}})
	call.Questions[1].Complete("", false, true)
	if got := QuestionReplies(&call); got != `<question_answers>[{"id":"q2","skipped":true}]</question_answers>` {
		t.Fatal(got)
	}
	call.Submitted()
	if call.Sent || call.Resolved || call.Questions[0].Outcome != "" || !call.Questions[1].Sent {
		t.Fatalf("skip affected siblings or settled before acknowledgement: %+v", call)
	}
	if got := QuestionReplies(&call); got != "" {
		t.Fatal("resent pending skip", got)
	}
}

func TestApprovalBindingAndEditorWithoutRenderer(t *testing.T) {
	request, err := binding.DecodeApprovalRequest(jsontext.Value(`{"threadId":"child","itemId":"command","availableDecisions":["accept","cancel"]}`))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeApproval("item/commandExecution/requestApproval", jsontext.Value(`3`), request, "/work", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var approvals prompt.Approvals
	approvals.Add(&decoded.Record)
	if approvals.ShouldOpen(false, false) || approvals.ShouldOpen(true, true) || !approvals.ShouldOpen(true, false) {
		t.Fatal("approval stole occupied editor")
	}
	var main composer.Editor
	main.InsertText("main draft")
	var editor prompt.Editor
	approvals.Show()
	editor.Open(&main, decoded.Record.Editor)
	main.InsertText("Use the other command")
	decoded.Record.Selected = decoded.Record.DenialChoice()
	choice := decoded.Record.Choices[decoded.Record.Selected]
	if decoded.Responses[choice.ID]["decision"] != "decline" {
		t.Fatal("denial changed host behavior")
	}
	decoded.Record.Editor = editor.Close(&main)
	approvals.Hide()
	if main.Text != "main draft" || decoded.Record.Editor.Snapshot.Text != "Use the other command" {
		t.Fatal("approval editor merged with main")
	}
	view := decoded.Record.View()
	view.Choices[0].Label = "changed"
	if decoded.Record.Choices[0].Label == "changed" {
		t.Fatal("approval view aliases choices")
	}
	approvals.Remove(&decoded.Record)
	if approvals.Show() {
		t.Fatal("resolved approval reopened")
	}
}
