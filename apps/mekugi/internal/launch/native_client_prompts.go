package launch

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

// nativeClientPrompts is renderer-local interaction state. Its domain values
// reuse Conversation's choice, completion and ordering rules without handles,
// execution state, or a second host request ledger.
type nativeClientPrompts struct {
	questions  prompt.Questions
	approvals  prompt.Approvals
	attachment uint64
	submitted  map[string]bool
}

func (p *nativeClientPrompts) update(frame *sessionapi.NativeFrame) {
	if p.attachment != frame.View.Version.Attachment {
		*p = nativeClientPrompts{attachment: frame.View.Version.Attachment}
	}
	calls := make([]*prompt.Call, 0, len(frame.Questions))
	for _, view := range frame.Questions {
		old := slices.IndexFunc(p.questions.Calls, func(call *prompt.Call) bool { return call.ID == view.ID })
		call := &prompt.Call{ID: view.ID, Thread: view.Thread, Turn: view.Turn, Item: view.Item, Sync: view.Sync, Local: view.Local}
		if old >= 0 {
			call = p.questions.Calls[old]
		} else {
			p.questions.AutoOpen = true
		}
		if call.Sent && !view.Sent {
			p.questions.AutoOpen = true
			delete(p.submitted, call.ID)
		}
		call.Sent, call.Resolved = view.Sent, view.Resolved
		previous := call.Questions
		call.Questions = make([]prompt.Question, len(view.Questions))
		for i, q := range view.Questions {
			current := prompt.Question{ID: q.ID, Header: q.Header, Question: q.Text, IsSecret: q.Secret, Choices: slices.Clone(q.Choices)}
			if old >= 0 && i < len(previous) && previous[i].ID == q.ID {
				current = previous[i]
			}
			if !q.Pending {
				current.Outcome, current.Answer, current.Editor = q.State, slices.Clone(q.Answer), composer.State{}
			}
			call.Questions[i] = current
		}
		calls = append(calls, call)
	}
	p.questions.Calls = calls
	if !slices.Contains(calls, p.questions.Active) || p.questions.Active != nil && (p.questions.Active.Sent || p.questions.Active.Resolved) {
		p.questions.Hide()
	}
	approvals := make([]*prompt.Approval, 0, len(frame.Approvals))
	for _, view := range frame.Approvals {
		old := slices.IndexFunc(p.approvals.Pending, func(approval *prompt.Approval) bool { return approval.ID == view.ID })
		approval := &prompt.Approval{ID: view.ID, Thread: view.Thread, Turn: view.Turn, Item: view.Item, Title: view.Title, Subject: view.Subject, Details: slices.Clone(view.Details), Choices: slices.Clone(view.Choices)}
		if old >= 0 {
			approval.Selected = p.approvals.Pending[old].Selected
		} else {
			p.approvals.AutoOpen = true
		}
		approvals = append(approvals, approval)
	}
	p.approvals.Pending = approvals
	if len(approvals) == 0 {
		p.approvals.Hide()
	}
}

func (p *nativeClientPrompts) overlay(view *sessionpresentation.View) {
	count := view.Questions.Count
	view.Questions = p.questions.View(view.Main.ID)
	view.Questions.Count = max(count, view.Questions.Count)
	view.Approvals = p.approvals.View()
	view.PromptDraft = conversation.PromptDraftView{}
	if call := view.Questions.Call; call != nil {
		view.PromptDraft.Call, view.PromptDraft.Question = call.ID, call.Questions[view.Questions.Index].ID
	} else if view.Approvals.Open {
		view.PromptDraft.Request = view.Approvals.Approval.ID
	}
	view.Draft.Available = view.PromptDraft.Call == "" && view.PromptDraft.Request == ""
}

func (c *nativeClient) openPrompts() {
	if c.event.Frame.View.ReadOnly != sessionpresentation.Editable {
		return
	}
	p := &c.prompts
	if len(p.approvals.Pending) > 0 {
		if p.approvals.Open {
			return
		}
		p.questions.Hide()
		p.approvals.Show()
	} else if p.questions.Active != nil || !p.questions.Open(p.questions.Next()) {
		return
	}
	c.renderer.terminal.Prompts.Reopen()
}

func (c *nativeClient) autoOpenPrompts() {
	view, p := c.event.Frame.View, &c.prompts
	if view.ReadOnly != sessionpresentation.Editable || view.Displayed != view.Main || view.Menu != nil || view.Panel != nil || view.Resume != nil || c.renderer.terminal.Main.Keybindings || c.renderer.terminal.Main.Pasting() || c.renderer.terminal.Focus != 0 {
		return
	}
	editor := c.editor("main")
	vacant := editor.Text == "" && len(editor.Images) == 0 && len(editor.Files) == 0 && len(editor.Skills) == 0 && len(editor.Selections) == 0
	if p.approvals.ShouldOpen(vacant, p.questions.Active != nil) {
		p.approvals.Show()
	}
	if !p.approvals.Open && p.questions.ShouldOpen(vacant) {
		p.questions.Open(p.questions.Next())
	}
}

// prepareComposerInput suspends unseen docks without changing auto-opening eligibility.
func (c *nativeClient) prepareComposerInput() bool {
	u := c.renderer.terminal
	preparation := u.Main.PrepareInput(u.Prompts.Ready())
	if !preparation.Editable {
		return false
	}
	if input := preparation.Question; input != nil && (!input.Version.SameAttachment(c.event.Frame.View.Version) || !c.prompts.questions.ApplyInput(input.QuestionInput)) {
		c.notice = sessionpresentation.ErrStaleIntent.Error()
		return false
	}
	if input := preparation.Approval; input != nil && (!input.Version.SameAttachment(c.event.Frame.View.Version) || !c.prompts.approvals.ApplyInput(input.ApprovalInput)) {
		c.notice = sessionpresentation.ErrStaleIntent.Error()
		return false
	}
	if preparation.Question != nil {
		c.prompts.questions.Hide()
	}
	if preparation.Approval != nil {
		c.prompts.approvals.Hide()
	}
	if preparation.Question != nil || preparation.Approval != nil {
		c.refreshView(time.Now())
	}
	return true
}

func (c *nativeClient) prompt(input terminalsession.PromptInput, next *terminalsession.KeyContinuation) bool {
	p := &c.prompts
	if !input.Version.SameAttachment(c.event.Frame.View.Version) {
		c.notice = sessionpresentation.ErrStaleIntent.Error()
		return true
	}
	editor, _ := c.selectedEditor()
	action := input.Action
	if input.Approval.Request != "" {
		if !p.approvals.ApplyInput(input.Approval) {
			c.notice = sessionpresentation.ErrStaleIntent.Error()
			return true
		}
		switch action {
		case conversationui.PromptPass, conversationui.PromptCursor:
			return false
		case conversationui.PromptHide:
			p.approvals.Hide()
			p.approvals.AutoOpen = false
		case conversationui.PromptSubmit:
			approval := p.approvals.Pending[0]
			intent := sessionpresentation.ApprovalIntent{Version: input.Version, Request: approval.ID, Choice: approval.Choices[approval.Selected].ID, Reason: editor.Text}
			c.enqueue(sessionapi.NativeOperation{Approval: &intent}, nil, func(err error) {
				if err == nil {
					p.approvals.Remove(approval)
					p.approvals.Hide()
					delete(c.editors, "prompt:::"+approval.ID)
					c.renderer.terminal.Prompts.ForgetApproval(approval.ID)
				} else if nativeAdmissionRefused(err) {
					if !c.retainsEditor(editor) {
						c.editor("main").Remember(editor.Draft)
					}
				} else if intent.Reason != "" {
					c.unknown = append(c.unknown, nativeUnknownInput{Draft: composer.Draft{Text: intent.Reason}, Thread: approval.Thread, Prompt: approval.ID})
				}
			})
		}
		return true
	}
	if !p.questions.ApplyInput(input.Question) {
		c.notice = sessionpresentation.ErrStaleIntent.Error()
		return true
	}
	switch action {
	case conversationui.PromptPass, conversationui.PromptCursor:
		return false
	case conversationui.PromptHide:
		p.questions.Hide()
		p.questions.AutoOpen = false
	case conversationui.PromptPrevious:
		p.questions.Move(-1)
	case conversationui.PromptNext:
		p.questions.Move(1)
	case conversationui.PromptAttach:
		_, target := c.selectedEditor()
		c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Version: input.Version, Target: target, Operation: sessionpresentation.AttachClipboard}})
	case conversationui.PromptAnswer, conversationui.PromptSkip:
		question, call := p.questions.Current(), p.questions.Active
		skip := action == conversationui.PromptSkip
		if question.IsSecret {
			answer := *question
			if !answer.Complete(editor.Text, call.Sync, skip) {
				c.notice = prompt.ErrEmptyAnswer.Error()
				return true
			}
			question.Done, question.Skipped = answer.Done, answer.Skipped
			c.renderer.terminal.Prompts.Secrets(call.ID).Answers[p.questions.Index] = answer.Answer
			c.renderer.terminal.Prompts.Secrets(call.ID).Editors[p.questions.Index] = editor.Park()
		} else {
			_, err := p.questions.Answer(prompt.AnswerInput{Call: call.ID, Question: input.Question.Question, Choice: question.Selected, Draft: editor.Draft.Clone(), Skip: skip})
			if err != nil {
				c.notice = err.Error()
				return true
			}
		}
		if step, submit := p.questions.Advance(); step != 0 {
			p.questions.Move(step)
		} else if submit {
			c.submitPrompt(false)
		}
	case conversationui.PromptSubmit:
		c.submitPrompt(true)
	}
	return true
}

func (c *nativeClient) submitPrompt(incomplete bool) {
	if !c.inputAvailable() {
		c.notice = "Previous input is still settling · answers kept in the editor"
		return
	}
	p := &c.prompts
	call, err := p.questions.Submission(p.questions.Active.ID, incomplete)
	if err != nil {
		c.notice = err.Error()
		return
	}
	input := sessionapi.NativeAnswers{Call: call.ID, Incomplete: incomplete}
	var ordinary []nativeUnknownInput
	var editors []*composer.Editor
	view := call.View()
	for i, question := range call.Questions {
		if question.Outcome != "" {
			continue
		}
		answer := sessionapi.NativeQuestionAnswer{AnswerInput: prompt.AnswerInput{Call: call.ID, Question: view.Questions[i].ID, Choice: question.Selected, Draft: question.Editor.Snapshot.Draft.Clone(), Skip: question.Skipped || !question.Done}}
		if question.IsSecret {
			answer.Draft = c.renderer.terminal.Prompts.SecretState(call.ID).Editors[i].Snapshot.Draft.Clone()
			answer.Secret, answer.Draft.Text = answer.Draft.Text, ""
		}
		if answer.Skip {
			answer.Draft, answer.Secret = composer.Draft{}, ""
		}
		input.Answers = append(input.Answers, answer)
		if !question.IsSecret {
			if editor := c.editors["prompt:"+call.ID+":"+question.ID+":"]; editor != nil {
				editors = append(editors, editor)
			}
			if !answer.Skip {
				draft := answer.Draft.Clone()
				if draft.Text == "" {
					draft.Text = strings.Join(question.Answer, "\n")
				}
				ordinary = append(ordinary, nativeUnknownInput{Draft: draft, Thread: call.Thread, Prompt: question.ID})
			}
		}
	}
	c.enqueue(sessionapi.NativeOperation{Answers: &input}, nil, func(err error) {
		if nativeAdmissionRefused(err) {
			for _, editor := range editors {
				if !c.retainsEditor(editor) {
					c.editor("main").Remember(editor.Draft)
				}
			}
			return
		}
		if err != nil {
			c.unknown = append(c.unknown, ordinary...)
			if slices.Contains(p.questions.Calls, call) {
				if p.submitted == nil {
					p.submitted = make(map[string]bool)
				}
				p.submitted[call.ID] = true
			}
			return
		}
		if call.Sent && slices.Contains(p.questions.Calls, call) {
			if p.submitted == nil {
				p.submitted = make(map[string]bool)
			}
			p.submitted[call.ID] = true
		}
		p.questions.AutoOpen = true
	})
}

// reconcilePromptEditors runs before replacing the detached prompt inventory.
// Externally retired ordinary drafts return to local history; secrets are erased.
func (c *nativeClient) reconcilePromptEditors(frame *sessionapi.NativeFrame) {
	for _, call := range c.prompts.questions.Calls {
		index := slices.IndexFunc(frame.Questions, func(view prompt.CallView) bool { return view.ID == call.ID })
		retained := c.prompts.attachment == frame.View.Version.Attachment && index >= 0

		submitted := c.prompts.submitted[call.ID] || c.pendingPrompt(call.ID, "")
		for questionIndex, question := range call.Questions {
			if retained {
				current := slices.IndexFunc(frame.Questions[index].Questions, func(view prompt.QuestionView) bool { return view.ID == question.ID })
				retired := current < 0 || !frame.Questions[index].Questions[current].Pending
				if !retired && (!frame.Questions[index].Sent || !question.IsSecret) {
					continue
				}
			}
			secrets := c.renderer.terminal.Prompts.SecretState(call.ID)
			delete(secrets.Editors, questionIndex)
			delete(secrets.Answers, questionIndex)
			key := "prompt:" + call.ID + ":" + question.ID + ":"
			if editor := c.editors[key]; editor != nil && !question.IsSecret && !submitted && editor.Text != "" {
				c.editor("main").Remember(editor.Draft.Clone())
			}
			delete(c.editors, key)
			secret := call.ID + ":" + question.ID
			delete(c.secrets, secret)
			if c.secretKey == secret {
				c.secretKey = ""
				c.renderer.terminal.Prompts.Secret = composer.Editor{}
			}
		}
		if !retained || frame.Questions[index].Sent {
			c.renderer.terminal.Prompts.ForgetCall(call.ID)
		}
		if !retained {
			delete(c.prompts.submitted, call.ID)
		}
	}
	for _, approval := range c.prompts.approvals.Pending {
		retained := c.prompts.attachment == frame.View.Version.Attachment && slices.ContainsFunc(frame.Approvals, func(view prompt.ApprovalView) bool { return view.ID == approval.ID })
		if retained {
			continue
		}
		key := "prompt:::" + approval.ID
		if editor := c.editors[key]; editor != nil && !c.pendingPrompt("", approval.ID) {
			c.editor("main").Remember(editor.Draft)
		}
		delete(c.editors, key)
		c.renderer.terminal.Prompts.ForgetApproval(approval.ID)
	}
}

// An outstanding request owns retired prompt editors until its admission result
// is known. The request callback preserves refused drafts or uncertain payloads.
func (c *nativeClient) pendingPrompt(question, approval string) bool {
	matches := func(call *nativeClientCall) bool {
		return call != nil && (question != "" && call.operation.Answers != nil && call.operation.Answers.Call == question || approval != "" && call.operation.Approval != nil && call.operation.Approval.Request == approval)
	}
	return matches(c.request(nativeInputCall)) || matches(c.request(nativeSemanticCall)) || slices.ContainsFunc(c.calls, matches)
}
