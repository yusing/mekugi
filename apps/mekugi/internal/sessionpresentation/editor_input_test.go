package sessionpresentation_test

import (
	"errors"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
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
	if _, err := c.model().ApplyEditor(old, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) {
		t.Fatalf("publication bypassed editor identity: %v", err)
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

func TestPreparedChildAllowsPromptEditing(t *testing.T) {
	c := newStatusConsumer()
	c.model().RestrictChildInput()
	call := &prompt.Call{ID: "ask", Thread: "main", Sync: true, Questions: []prompt.Question{{ID: "question"}}}
	c.conversation.Questions.Add(call, false)
	c.conversation.Questions.Open(call)
	v := c.publish(sessionpresentation.Caption{})
	target := v.EditorTarget()
	if err := c.model().AdmitPaste(sessionpresentation.PasteIntent{Version: v.Version, Target: target}, &c.conversation); err != nil {
		t.Fatal(err)
	}
	if err := c.model().AdmitClientComposer(sessionpresentation.ComposerIntent{Version: v.Version, Target: target, Operation: sessionpresentation.SubmitComposer}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.model().ApplyEditor(sessionpresentation.EditorIntent{Version: v.Version, Target: target, Action: composer.Action{Kind: composer.InsertAction, Text: "answer"}}, &c.conversation); err != nil {
		t.Fatal(err)
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
	in := sessionpresentation.ReferenceIntent{Version: v.Version, Displayed: v.Displayed, Target: v.EditorTarget(), Text: "selected source"}
	c.model().Status.Set("More progress", false)
	if next := c.publish(sessionpresentation.Caption{}); next.Version == v.Version {
		t.Fatal("fixture did not advance the publication")
	}
	c.conversation.Editor.Replace(composer.Draft{Text: "replacement"})
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) || c.model().View().Displayed != child {
		t.Fatalf("stale editor changed display: %v", err)
	}
	v = c.publish(sessionpresentation.Caption{})
	in.Version, in.Target, in.Displayed = v.Version, v.EditorTarget(), v.Displayed
	c.model().Status.Set("Still progressing", false)
	c.publish(sessionpresentation.Caption{})
	if err := c.model().Reference(in, &c.conversation); err != nil || c.model().View().Displayed != v.Main || c.conversation.Editor.Text != "replacement" {
		t.Fatalf("reference admission: %v", err)
	}
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("old reference was readmitted: %v", err)
	}
	c.model().Attach(v.Main, true)
	v = c.publish(sessionpresentation.Caption{})
	in.Version, in.Target, in.Displayed = v.Version, v.EditorTarget(), v.Displayed
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, sessionpresentation.ErrReplayIntent) {
		t.Fatalf("replay admitted reference: %v", err)
	}
}

// A detached draft follows its captured input destination, not display progress
// or another editor's revision. Admission still uses current owner gates.
func TestBoundReferenceRetainsOriginalEditorAfterNavigation(t *testing.T) {
	c := newStatusConsumer()
	v := c.publish(sessionpresentation.Caption{})
	in := sessionpresentation.ReferenceIntent{Version: v.Version, Thread: v.Input.ID, Displayed: v.Displayed, Target: v.EditorTarget(), Text: "selected source"}
	child := sessionpresentation.Thread{ID: "child", Caller: "/root/child"}
	if err := c.model().Display(sessionpresentation.DisplayIntent{Version: v.Version, Thread: child}); err != nil {
		t.Fatal(err)
	}
	if err := c.model().Reference(in, &c.conversation); err != nil || c.model().View().Displayed != child {
		t.Fatalf("bound reference lost its editor or changed navigation: %v", err)
	}
	c.conversation.Editor.Replace(composer.Draft{Text: "new draft"})
	if err := c.model().Reference(in, &c.conversation); !errors.Is(err, conversationmodel.ErrDraftChanged) {
		t.Fatalf("bound reference ignored editor replacement: %v", err)
	}
}

func TestClientComposerProgressRetainsDestinationAndCurrentGates(t *testing.T) {
	c := newStatusConsumer()
	v := c.publish(sessionpresentation.Caption{})
	intent := sessionpresentation.ComposerIntent{Version: v.Version, Thread: v.Input.ID, Operation: sessionpresentation.SubmitComposer}
	c.model().Status.Set("More progress", false)
	if next := c.publish(sessionpresentation.Caption{}); next.Version == v.Version {
		t.Fatal("fixture did not advance the publication")
	}
	if plan, err := c.model().ClientInput(intent, &c.control, &c.conversation, false); err != nil || !plan.Flush || plan.Queued {
		t.Fatalf("progress invalidated steering: %+v, %v", plan, err)
	}
	restore := intent
	restore.Operation, restore.Work = sessionpresentation.RestoreWaiting, new(v.Interrupt)
	if err := c.model().AdmitClientComposer(restore); err != nil {
		t.Fatalf("progress invalidated restoration for the same work: %v", err)
	}
	for _, operation := range []sessionpresentation.ComposerOperation{sessionpresentation.IncreaseEffort, sessionpresentation.DecreaseEffort} {
		controlIntent := intent
		controlIntent.Operation = operation
		if err := c.model().AdmitClientComposer(controlIntent); err != nil {
			t.Fatalf("progress invalidated effort adjustment: %v", err)
		}
	}
	for _, operation := range []sessionpresentation.ComposerOperation{sessionpresentation.CancelComposer, sessionpresentation.RestoreWaiting} {
		controlIntent := intent
		controlIntent.Operation = operation
		if err := c.model().AdmitClientComposer(controlIntent); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
			t.Fatalf("stale composer control %v acted on current work or settings: %v", operation, err)
		}
	}
	c.control.Thread = ""
	if _, err := c.model().ClientInput(intent, &c.control, &c.conversation, false); err == nil {
		t.Fatal("old editable frame bypassed the current control gate")
	}
	c.control.Thread = "main"
	side := sessionpresentation.Thread{ID: "side", Caller: "/root/side"}
	c.model().SetSide(side)
	if err := c.model().Display(sessionpresentation.DisplayIntent{Version: c.model().View().Version, Thread: side}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.model().ClientInput(intent, &c.control, &c.conversation, false); err != nil {
		t.Fatalf("navigation invalidated a pending Main submission: %v", err)
	}
	c.store = scope.Store{}
	c.model().Attach(v.Main, false)
	c.publish(sessionpresentation.Caption{})
	if _, err := c.model().ClientInput(intent, &c.control, &c.conversation, false); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatalf("replacement attachment accepted old composer: %v", err)
	}
}

func TestInputDestinationLifetimeIgnoresProgressAndLabels(t *testing.T) {
	c := newStatusConsumer()
	c.conversation.Editor.Replace(composer.Draft{Text: "draft"})
	v := c.publish(sessionpresentation.Caption{})
	edit := sessionpresentation.EditorIntent{Version: v.Version, Target: v.EditorTarget(), Action: composer.Action{Kind: composer.InsertAction, Text: " retained"}}
	c.model().Status.Set("streaming output", false)
	c.publish(sessionpresentation.Caption{})
	c.model().RenameCaller("/root", "/root/renamed")
	if _, err := c.model().ApplyEditor(edit, &c.conversation); err != nil || c.conversation.Editor.Text != "draft retained" {
		t.Fatalf("progress or caller label invalidated the editor: %q %v", c.conversation.Editor.Text, err)
	}
	version := c.model().View().Version
	future := version
	future.Revision++
	if err := c.model().AdmitDestination(future); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatal("future observation admitted")
	}
	side := sessionpresentation.Thread{ID: "side", Caller: "/root/side"}
	c.model().SetSide(side)
	if err := c.model().Display(sessionpresentation.DisplayIntent{Version: version, Thread: side}); err != nil {
		t.Fatal(err)
	}
	selected := c.model().View().Version
	c.model().RenameCaller(side.Caller, "/root/renamed-side")
	if !c.model().DisplayingSide() || c.model().AdmitDestination(selected) != nil {
		t.Fatal("caller rename replaced the side destination")
	}
	c.model().SetSide(sessionpresentation.Thread{})
	if err := c.model().AdmitDestination(selected); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatal("removed side retained its implicit input destination")
	}
	if err := c.model().AdmitDestination(version); !errors.Is(err, sessionpresentation.ErrStaleIntent) {
		t.Fatal("returning to Main revived an unqualified external action")
	}
}

func TestPreparedChildRejectsBoundOrdinaryInput(t *testing.T) {
	c := newStatusConsumer()
	c.model().RestrictChildInput()
	v := c.publish(sessionpresentation.Caption{})
	intent := sessionpresentation.ComposerIntent{Version: v.Version, Thread: v.Main.ID, Target: v.EditorTarget(), Operation: sessionpresentation.SubmitComposer}
	if err := c.model().AdmitBoundInput(intent); !errors.Is(err, sessionpresentation.ErrInputUnavailable) {
		t.Fatalf("bound child input admitted: %v", err)
	}
	if _, err := c.model().ClientInput(intent, &c.control, &c.conversation, false); !errors.Is(err, sessionpresentation.ErrInputUnavailable) {
		t.Fatalf("bound child submission planned: %v", err)
	}
}
