package terminalsession_test

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestPromptOpeningReceipt(t *testing.T) {
	var p terminalsession.Prompts
	var main terminalsession.Main
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1}}
	v.Questions.Call = &prompt.CallView{ID: "ask", Questions: []prompt.QuestionView{{ID: "q"}}}
	first := p.Update(v)
	main.Update(v)
	if preparation := main.PrepareInput(p.Ready()); !preparation.Editable || preparation.Question == nil || preparation.Question.Version != v.Version || preparation.Question.Call != "ask" || preparation.Question.Question != "q" {
		t.Fatal("unseen question did not retain its suspension target")
	}
	if p.Ready() {
		t.Fatal("publication acknowledged display")
	}
	p.Delivered(first)
	if preparation := main.PrepareInput(p.Ready()); preparation.Question != nil || !preparation.Editable {
		t.Fatal("delivered prompt yielded its editor")
	}
	v.Version.Revision++
	if p.Update(v) != first || !p.Ready() {
		t.Fatal("ordinary update invalidated visible prompt")
	}
	p.Reopen()
	second := p.Update(v)
	p.Delivered(first)
	if second == first || p.Ready() {
		t.Fatal("old frame readied reopened prompt")
	}
	p.Delivered(second)
	v.Version.Attachment++
	p.Update(v)
	p.Delivered(second)
	if p.Ready() {
		t.Fatal("old attachment readied replacement")
	}
	v.Questions.Call = nil
	v.Approvals = prompt.ApprovalDockView{Open: true, Approval: prompt.ApprovalView{ID: "approve"}}
	approval := p.Update(v)
	p.Delivered(approval)
	if !p.Ready() {
		t.Fatal("delivered approval stayed blocked")
	}
	v.Approvals.Open = false
	p.Delivered(p.Update(v))
	if p.Ready() {
		t.Fatal("hidden prompt became ready")
	}
}

func TestPromptLayoutIdentityAndSecretRetirement(t *testing.T) {
	var p terminalsession.Prompts
	dock := prompt.QuestionDockView{Call: &prompt.CallView{ID: "call", Questions: []prompt.QuestionView{{ID: "first"}, {ID: "second"}}}}
	layout := p.QuestionLayout(dock)
	layout.Column = new(7)
	copy := dock
	call := *dock.Call
	copy.Call = &call
	if p.QuestionLayout(copy) != layout {
		t.Fatal("detached publication replaced layout")
	}
	copy.Index = 1
	if p.QuestionLayout(copy) == layout {
		t.Fatal("questions shared cursor state")
	}
	p.Secret.Insert("private")
	p.Secrets("call").Editors[0] = p.Secret.Park()
	p.Secrets("call").Answers[0] = []string{"private"}
	p.ForgetCall("call")
	state := p.SecretState("call")
	if len(state.Editors) != 0 || len(state.Answers) != 0 || p.QuestionLayout(dock) == layout {
		t.Fatal("retired prompt retained state")
	}
}
