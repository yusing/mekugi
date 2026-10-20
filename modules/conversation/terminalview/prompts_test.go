package terminalview_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/conversation/terminalview"
)

func TestQuestionKeyViewAndDraftIsolation(t *testing.T) {
	var questions prompt.Questions
	call := &prompt.Call{ID: "ask", Thread: "main", Sync: true, Questions: []prompt.Question{{Question: "Choose?", Choices: []prompt.Option{{Label: "Yes"}}}, {Question: "Password?", IsSecret: true}}}
	questions.Add(call, false)
	questions.Open(questions.Next())
	var layout terminalview.QuestionLayout
	key := func(text string, empty bool) terminalview.PromptAction {
		view := questions.View("main")
		result := layout.Key(view, text, empty)
		if !reflect.DeepEqual(view, questions.View("main")) {
			t.Fatal("key interpretation mutated source")
		}
		if !questions.ApplyInput(result.Question) {
			t.Fatal("targeted question edit rejected")
		}
		return result.Action
	}
	if key("\t", true) != terminalview.PromptHandled || !questions.Current().Note {
		t.Fatal("synchronous option note not selected")
	}
	if key("\x1b[C", true) != terminalview.PromptCursor {
		t.Fatal("note navigation moved the question")
	}
	key("\x1b", true)
	if questions.Current().Note || key("\x1b[C", true) != terminalview.PromptNext {
		t.Fatal("note dismissal did not restore question navigation")
	}
	questions.Move(1)
	questions.Current().Answer = []string{"secret bytes"}
	questions.Current().Outcome = "answered"
	view := questions.View("main")
	view.Call.Questions[0].Choices[0].Label = "detached"
	if call.Questions[0].Choices[0].Label != "Yes" || len(view.Call.Questions[1].Answer) != 0 {
		t.Fatal("dock view aliases choices or exposes secrets")
	}
	rows := terminalview.Questions(view, &terminalui.Painter{}, &layout, "", false, 50, 12)
	if strings.Contains(strings.Join(rows, "\n"), "secret bytes") || len(rows) > 12 {
		t.Fatal("rendered secret or exceeded dock bounds")
	}
}

func TestApprovalKeyKeepsHostEffectsWithCaller(t *testing.T) {
	approval := &prompt.Approval{Choices: []prompt.Choice{{ID: 0, Label: "Once"}, {ID: 1, Label: "Session"}, {ID: 2, Label: "Decline", Deny: true}}}
	var layout terminalview.ApprovalLayout
	var approvals prompt.Approvals
	approvals.Add(approval)
	approvals.Show()
	key := func(text string, empty bool) terminalview.PromptAction {
		view := approval.View()
		result := layout.Key(view, text, empty)
		if !reflect.DeepEqual(view, approval.View()) {
			t.Fatal("key interpretation mutated source")
		}
		if !approvals.ApplyInput(result.Approval) {
			t.Fatal("targeted approval edit rejected")
		}
		return result.Action
	}
	if key("x", true) != terminalview.PromptPass || approval.Selected != 2 {
		t.Fatal("typed reason did not select denial")
	}
	if key("1", false) != terminalview.PromptPass || approval.Selected != 2 {
		t.Fatal("digit in reason changed the offered choice")
	}
	if key("2", true) != terminalview.PromptHandled || approval.Selected != 1 || key("\r", true) != terminalview.PromptSubmit {
		t.Fatal("session approval intent lost its selected scope")
	}
	approval.Choices = []prompt.Choice{{Label: "Accept"}, {Label: "Cancel"}}
	if key("x", true) != terminalview.PromptHandled || key("\x03", true) != terminalview.PromptPass || key("\x02", true) != terminalview.PromptPass {
		t.Fatal("non-reason approval edited Main or swallowed session controls")
	}
}
