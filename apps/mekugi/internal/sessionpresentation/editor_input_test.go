package sessionpresentation_test

import (
	"errors"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestEditorActionsPreserveGroupingAndRejectStaleTargets(t *testing.T) {
	c := newStatusConsumer()
	c.conversation.Editor.Replace(composer.Draft{})
	intent := func(action composer.Action) sessionpresentation.EditorIntent {
		v := c.publish(sessionpresentation.Caption{})
		return sessionpresentation.EditorIntent{Version: v.Version, Target: conversationmodel.EditorTarget{Revision: v.Draft.Revision}, Action: action}
	}
	apply := func(action composer.Action) {
		t.Helper()
		if _, err := c.model().ApplyEditor(intent(action), &c.conversation); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"first", " ", "second"} {
		apply(composer.Action{Kind: composer.InsertAction, Text: text})
	}
	apply(composer.Action{Kind: composer.UndoAction})
	if c.conversation.Editor.Text != "first " {
		t.Fatal("typed-word grouping changed")
	}
	apply(composer.Action{Kind: composer.RedoAction})
	old := intent(composer.Action{Kind: composer.InsertAction, Text: "stale"})
	apply(composer.Action{Kind: composer.MoveAction, Start: 0})
	if _, err := c.model().ApplyEditor(old, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) || c.conversation.Editor.Text != "first second" {
		t.Fatalf("stale caret target: %v", err)
	}
	c.publish(sessionpresentation.Caption{})
	if _, err := c.model().ApplyEditor(old, &c.conversation); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("stale publication: %v", err)
	}
	c.conversation.Editor.Replace(composer.Draft{Text: "@file", Files: []composer.Token{{Start: 0, End: 5, Path: "file"}}})
	bad := intent(composer.Action{Kind: composer.DeleteAction, Start: 1, End: 5})
	before := c.conversation.Editor.Revision()
	if _, err := c.model().ApplyEditor(bad, &c.conversation); err == nil || c.conversation.Editor.Revision() != before || c.conversation.Editor.Text != "@file" {
		t.Fatal("invalid token deletion changed the editor")
	}
	command := sessionpresentation.ComposerIntent{Version: bad.Version, Target: bad.Target, Operation: sessionpresentation.SubmitComposer}
	if err := c.model().AdmitComposer(command, &c.conversation); err != nil {
		t.Fatal(err)
	}
	c.conversation.Editor.Replace(composer.Draft{Text: "replacement"})
	if err := c.model().AdmitComposer(command, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) {
		t.Fatalf("stale operation: %v", err)
	}
	c.model().Attach(c.model().View().Main, true)
	bad.Version = c.model().View().Version
	if _, err := c.model().ApplyEditor(bad, &c.conversation); !errors.Is(err, sessionpresentation.ErrInputUnavailable) {
		t.Fatalf("replay edit: %v", err)
	}
}

func TestEditorActionsTargetPromptAndExcludeSecrets(t *testing.T) {
	c := newStatusConsumer()
	call := &prompt.Call{ID: "ask", Thread: "main", Sync: true, Questions: []prompt.Question{{ID: "first"}, {ID: "second"}, {ID: "secret", IsSecret: true}}}
	c.conversation.Questions.Add(call, false)
	c.conversation.Questions.Open(call)
	v := c.publish(sessionpresentation.Caption{})
	in := sessionpresentation.EditorIntent{Version: v.Version, Target: conversationmodel.EditorTarget{Call: v.PromptDraft.Call, Question: v.PromptDraft.Question, Revision: v.PromptDraft.Revision}, Action: composer.Action{Kind: composer.InsertAction, Text: "answer"}}
	if _, err := c.model().ApplyEditor(in, &c.conversation); err != nil {
		t.Fatal(err)
	}
	c.conversation.Questions.Move(1)
	in.Target.Revision = c.conversation.Editor.Revision()
	if _, err := c.model().ApplyEditor(in, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) {
		t.Fatalf("replacement question accepted an old target: %v", err)
	}
	c.conversation.Questions.Move(1)
	v = c.publish(sessionpresentation.Caption{})
	in.Version = v.Version
	in.Target = conversationmodel.EditorTarget{Call: v.PromptDraft.Call, Question: v.PromptDraft.Question, Revision: v.PromptDraft.Revision}
	if _, err := c.model().ApplyEditor(in, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) {
		t.Fatalf("neutral editor accepted a secret: %v", err)
	}
}

func TestReferenceAdmitsReturnToMainAndOriginalEditorTogether(t *testing.T) {
	c := newStatusConsumer()
	v := c.publish(sessionpresentation.Caption{})
	child := sessionpresentation.Thread{ID: "child", Caller: "/root/child"}
	if err := c.model().Display(sessionpresentation.DisplayIntent{Version: v.Version, Thread: child}); err != nil {
		t.Fatal(err)
	}
	v = c.publish(sessionpresentation.Caption{})
	in := sessionpresentation.ReferenceIntent{Version: v.Version, Target: v.EditorTarget(), Text: "selected source"}
	c.conversation.Editor.Replace(composer.Draft{Text: "replacement"})
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) || c.model().View().Displayed != child {
		t.Fatalf("stale editor changed display: %v", err)
	}
	v = c.publish(sessionpresentation.Caption{})
	in.Version, in.Target = v.Version, v.EditorTarget()
	if err := c.model().Reference(in, &c.conversation); err != nil || c.model().View().Displayed != v.Main || c.conversation.Editor.Text != "replacement" {
		t.Fatalf("reference admission: %v", err)
	}
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("old reference was readmitted: %v", err)
	}
	c.model().Attach(v.Main, true)
	v = c.publish(sessionpresentation.Caption{})
	in.Version, in.Target = v.Version, v.EditorTarget()
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, sessionpresentation.ErrReplayIntent) {
		t.Fatalf("replay admitted reference: %v", err)
	}
}
