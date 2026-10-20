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
	call := Questions("main", "turn", "ask", nil, []binding.Question{{Title: "Audience?", Options: jsontext.Value(`["Developers"]`)}, {Title: "Why?"}})
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
	call.SubmissionID = "sent"
	for _, r := range binding.QuestionReplies(reply) {
		item, index, ok := binding.QuestionReplyIdentity(r)
		if !ok || item != "ask" {
			t.Fatal("unstable wire identity")
		}
		call.Commit(index, "sent", r.Answer)
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
	_ = json.Unmarshal([]byte(`{"answers":{"q":{"answers":["Yes","user_note: user_note: literal"]},"secret":{"answers":["user_note: secret bytes"]}}}`), &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire response: %s", data)
	}
	call.Submitted()
	call.Resolve("answered")
	if call.Questions[0].Answer[1] != "user_note: literal" || len(call.View().Questions[1].Answer) != 0 || call.Questions[1].Editor.Snapshot.Text != "" {
		t.Fatal("wire encoding or secrets entered neutral view")
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
