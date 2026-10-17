package sessionpresentation_test

import (
	"errors"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestNonTerminalPromptInputAndDraftPublication(t *testing.T) {
	c := newStatusConsumer()
	questions := &c.conversation.Questions
	call := &prompt.Call{ID: "ask", Thread: "main", Sync: true, Questions: []prompt.Question{{ID: "choice", Choices: []prompt.Option{{Label: "yes"}}}, {ID: "password", IsSecret: true}}}
	questions.Add(call, false)
	questions.Open(call)
	c.conversation.Editor.Replace(composer.Draft{Text: "answer", Files: []composer.Token{{Path: "file"}}})
	v := c.publish(sessionpresentation.Caption{})
	if v.Draft.Available || v.PromptDraft.Call != call.ID || v.PromptDraft.Question != "ask/0" || v.PromptDraft.Draft.Text != "answer" {
		t.Fatal("active editor target or ordinary input isolation lost")
	}
	v.PromptDraft.Draft.Files[0].Path = "changed"
	if c.model().View().PromptDraft.Draft.Files[0].Path != "file" {
		t.Fatal("editor publication aliases retained data")
	}
	intent := sessionpresentation.QuestionInputIntent{Version: v.Version, QuestionInput: prompt.QuestionInput{Call: call.ID, Question: "ask/0", Kind: prompt.InputNote, Enabled: true}}
	if err := c.model().QuestionInput(intent, questions); err != nil || !questions.Current().Note {
		t.Fatalf("note input: %v", err)
	}
	c.publish(sessionpresentation.Caption{})
	intent.Enabled = false
	if err := c.model().QuestionInput(intent, questions); !errors.Is(err, sessionpresentation.ErrStaleIntent) || !questions.Current().Note {
		t.Fatal("stale input changed current question")
	}
	intent.Version = c.model().View().Version
	intent.Question = "retired"
	if err := c.model().QuestionInput(intent, questions); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatal("wrong identity admitted")
	}
	questions.Move(1)
	if secret := c.publish(sessionpresentation.Caption{}).PromptDraft; secret.Question != "ask/1" || secret.Draft.Text != "" || len(secret.Draft.Files) != 0 || secret.Revision != 0 {
		t.Fatal("secret prompt published ordinary editor contents")
	}
	questions.Hide()
	approvals := &c.conversation.Approvals
	approvals.Add(&prompt.Approval{ID: "approval", Choices: []prompt.Choice{{ID: 10, Label: "yes"}, {ID: 20, Label: "no", Deny: true}}})
	approvals.Show()
	v = c.publish(sessionpresentation.Caption{})
	if v.PromptDraft.Request != "approval" || v.PromptDraft.Draft.Text != "answer" {
		t.Fatal("approval editor target lost")
	}
	a := sessionpresentation.ApprovalInputIntent{Version: v.Version, ApprovalInput: prompt.ApprovalInput{Request: "approval", Select: true, Choice: 20}}
	if err := c.model().ApprovalInput(a, approvals); err != nil || approvals.Pending[0].Selected != 1 {
		t.Fatalf("approval input: %v", err)
	}
	a.Choice = 99
	if err := c.model().ApprovalInput(a, approvals); !errors.Is(err, sessionpresentation.ErrStaleIntent) || approvals.Pending[0].Selected != 1 {
		t.Fatal("unoffered approval choice admitted")
	}
	c.model().Attach(v.Main, true)
	a.Version = c.model().View().Version
	if err := c.model().ApprovalInput(a, approvals); !errors.Is(err, sessionpresentation.ErrReplayIntent) {
		t.Fatal("replay admitted prompt input")
	}
}
