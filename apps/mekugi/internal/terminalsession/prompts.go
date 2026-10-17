package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

// Source: apps/mekugi/internal/router/app_server_questions.go:28:106,
// app_server_approvals.go:27:53 and native_shell.go:237:253@1b46b141.
// Prompts retains terminal interaction only. Ordinary editors belong to Conversation.
type Prompts struct {
	Rect      Rect
	Secret    composer.Editor
	questions map[questionKey]*conversationui.QuestionLayout
	approvals map[string]*conversationui.ApprovalLayout
	secrets   map[string]*SecretPrompt
	receipt   PromptReceipt
	ready     bool
}

type questionKey struct{ call, question string }

// SecretPrompt never enters a session view, retained record or playback event.
type SecretPrompt struct {
	Editors map[int]composer.State
	Answers map[int][]string
}

type PromptReceipt struct {
	Attachment, Opening uint64
	Call, Request       string
}

func (p *Prompts) QuestionLayout(d prompt.QuestionDockView) *conversationui.QuestionLayout {
	if d.Call == nil {
		return nil
	}
	key := questionKey{d.Call.ID, d.Call.Questions[d.Index].ID}
	if p.questions == nil {
		p.questions = make(map[questionKey]*conversationui.QuestionLayout)
	}
	if p.questions[key] == nil {
		p.questions[key] = new(conversationui.QuestionLayout)
	}
	return p.questions[key]
}

func (p *Prompts) ApprovalLayout(id string) *conversationui.ApprovalLayout {
	if p.approvals == nil {
		p.approvals = make(map[string]*conversationui.ApprovalLayout)
	}
	if p.approvals[id] == nil {
		p.approvals[id] = new(conversationui.ApprovalLayout)
	}
	return p.approvals[id]
}

func (p *Prompts) Secrets(call string) *SecretPrompt {
	if p.secrets == nil {
		p.secrets = make(map[string]*SecretPrompt)
	}
	if p.secrets[call] == nil {
		p.secrets[call] = &SecretPrompt{Editors: make(map[int]composer.State), Answers: make(map[int][]string)}
	}
	return p.secrets[call]
}

// SecretState borrows invocation-local buffers only for input and authenticated delivery.
func (p *Prompts) SecretState(call string) SecretPrompt {
	if state := p.secrets[call]; state != nil {
		return *state
	}
	return SecretPrompt{}
}

func (p *Prompts) ForgetCall(call string) {
	delete(p.secrets, call)
	for key := range p.questions {
		if key.call == call {
			delete(p.questions, key)
		}
	}
}

func (p *Prompts) ForgetApproval(id string) { delete(p.approvals, id) }

// Reopen also invalidates a hide/reopen of the same prompt between publications.
func (p *Prompts) Reopen() { p.receipt.Opening++; p.ready = false }

func (p *Prompts) Update(v sessionpresentation.View) PromptReceipt {
	call, request := "", ""
	if v.Questions.Call != nil {
		call = v.Questions.Call.ID
	}
	if v.Approvals.Open {
		request = v.Approvals.Approval.ID
	}
	if p.receipt.Attachment != v.Version.Attachment || p.receipt.Call != call || p.receipt.Request != request {
		p.Reopen()
		p.receipt.Attachment, p.receipt.Call, p.receipt.Request = v.Version.Attachment, call, request
	}
	return p.receipt
}

func (p *Prompts) Delivered(receipt PromptReceipt) {
	if receipt == p.receipt && (receipt.Call != "" || receipt.Request != "") {
		p.ready = true
	}
}

func (p *Prompts) Ready() bool { return p.ready }
