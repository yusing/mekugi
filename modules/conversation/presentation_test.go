package conversation_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestConversationScopedPayloads(t *testing.T) {
	service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
	var store, other scope.Store
	m := service.Session(&store)
	reply := &prompt.Call{ID: "question", Sent: true}
	m.Unsent = []presentation.Input{{Text: "answer envelope", Question: reply, ReplyCount: 1}, {Text: "first"}, {Text: "second"}, {Text: "/compact", Compact: new(presentation.CompactIntent{})}}
	m.Queued = []presentation.Input{{Text: "next turn"}}
	if !m.AdvancePrompt() {
		t.Fatal("ordinary prompt did not pass deferred answer")
	}
	parts, rest := presentation.Batch(m.Unsent)
	if presentation.Join(parts...).Text != "first\nsecond" || len(rest) != 2 || rest[0].Question != reply {
		t.Fatalf("batch lost order or question identity: %+v %+v", parts, rest)
	}
	parts[0].Text = "[Image 1] first"
	parts[0].Images = []composer.Token{{Start: 0, End: 9, Path: "owned.png"}}
	parts[0].Attachments = []string{"immutable file bytes"}
	m.Stage("submission", presentation.Submission{Draft: presentation.Join(parts...).Draft, Parts: parts})
	parts[0].Images[0].Path = "changed.png"
	parts[0].Attachments[0] = "reopened file bytes"
	if got := m.Submitted["submission"]; got.Images[0].Path != "owned.png" || got.Attachments[0] != "immutable file bytes" {
		t.Fatalf("submitted payload changed: %+v", got)
	}
	m.Unsent = rest
	view := m.Pending([]string{"submission"}, presentation.Steering)
	view.Waiting[0].Text = "changed view"
	if m.Submitted["submission"].Text != "[Image 1] first\nsecond" || view.Waiting[2].Replies != 1 {
		t.Fatalf("pending view is not detached or omitted reply semantics: %+v", view)
	}
	m.Retain([]string{"submission"})
	if len(m.Submitted) != 1 {
		t.Fatal("retired a host-owned payload")
	}
	m.Retain(nil)
	if len(m.Submitted) != 0 {
		t.Fatal("settled payload remains retained")
	}
	waiting := m.TakeWaiting()
	if len(waiting) != 3 || waiting[0].Question != reply || waiting[2].Text != "next turn" || len(m.Unsent)+len(m.Queued) != 0 {
		t.Fatalf("withdrawal lost waiting input: %+v", waiting)
	}
	if service.Session(&other) == m || len(service.Session(&other).Unsent) != 0 {
		t.Fatal("scope state leaked")
	}
	store = scope.Store{}
	if service.Session(&store) == m || len(service.Session(&store).Submitted) != 0 {
		t.Fatal("replacement revived payloads")
	}
}

func TestConversationRestoreInputPreservesPromptEditor(t *testing.T) {
	for _, approval := range []bool{false, true} {
		service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
		var store scope.Store
		m := service.Session(&store)
		m.Editor.Replace(composer.Draft{Text: "ordinary draft"})
		m.PromptEditor.Open(&m.Editor, composer.State{})
		m.Editor.Replace(composer.Draft{Text: "unfinished prompt answer", CursorBack: 3})
		if approval {
			m.Approvals.Add(&prompt.Approval{ID: "approval", Choices: []prompt.Choice{{Deny: true}}})
			m.Approvals.Show()
		} else {
			call := &prompt.Call{ID: "question", Questions: []prompt.Question{{Question: "Continue?"}}}
			m.Questions.Add(call, false)
			m.Questions.Open(call)
		}
		restored := m.RestoreInput(presentation.Input{Text: "withdrawn input"})
		if !restored.Parked || !restored.Changed || m.Editor.Text != "unfinished prompt answer" || m.Editor.CursorBack != 3 {
			t.Fatalf("active prompt changed: approval=%v %+v", approval, restored)
		}
		m.PromptEditor.Close(&m.Editor)
		if m.Editor.Text != "withdrawn input\nordinary draft" {
			t.Fatalf("Main restoration: %q", m.Editor.Text)
		}
	}
}

func TestConversationSidePresentation(t *testing.T) {
	service := moduletest.New(t, conversation.Module, conversation.Deps{}, module.Slots{}, new(moduletest.Scripted))
	var store scope.Store
	m := service.Session(&store)
	m.Side = new(presentation.SideAnswer)
	side := m.Side
	side.Begin("question", presentation.Input{Text: "[Image 1]", Images: []composer.Token{{Start: 0, End: 9, Path: "owned.png"}}})
	side.Append("answer", "partial", false)
	side.Append("answer", "complete", true)
	if side.Answer[0].Text != "complete" {
		t.Fatal("completed answer did not replace streamed content")
	}
	side.Append("answer", strings.Repeat("界", 100_000), false)
	if len(side.Answer[0].Text) > 256<<10 || !utf8.ValidString(side.Answer[0].Text) || !side.Truncated {
		t.Fatal("side display bound split UTF-8")
	}
	restored := side.Fail(presentation.SideCompactionRequired, false)
	if restored.Text != "[Image 1]" || restored.Images[0].Path != "owned.png" || restored.Images[0].Start != 0 || side.Pending.Text != "" {
		t.Fatalf("side rejection lost attachment: %+v", restored)
	}
	side.Begin("follow-up", presentation.Input{Text: "follow-up"})
	side.Close()
	if restored := side.Fail("late failure", true); restored.Text != "" || len(side.Answer) != 0 {
		t.Fatal("closed side answer restored late input")
	}
}

func TestConversationSideHistory(t *testing.T) {
	var side presentation.SideAnswer
	side.Begin("first", presentation.Input{})
	side.Append("same-id", "first answer", true)
	side.Complete("failed", "first failure")
	side.Begin("second", presentation.Input{})
	side.Append("same-id", "second answer", true)
	if len(side.History) != 1 || side.History[0].Question != "first" || side.History[0].Answer[0].Text != "first answer" || side.History[0].Failure != "first failure" || side.Answer[0].Text != "second answer" {
		t.Fatalf("lost exchange: %+v", side)
	}
	for range 140 {
		side.Begin("next", presentation.Input{})
		side.Append("same-id", "answer", true)
	}
	if len(side.History) != 127 || !side.Truncated {
		t.Fatalf("item bound: history=%d truncated=%v", len(side.History), side.Truncated)
	}
	side.Append("same-id", strings.Repeat("界", 100_000), true)
	if len(side.History) != 0 || len(side.Question)+len(side.Answer[0].Text) > 256<<10 || !utf8.ValidString(side.Answer[0].Text) {
		t.Fatal("byte bound failed")
	}
	side.Close()
	if len(side.History) != 0 || len(side.Answer) != 0 {
		t.Fatal("close retained conversation")
	}
}
