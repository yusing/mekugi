package terminalsession

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestComposerKeyReturnsVersionedIntentAndKeepsSecretsLocal(t *testing.T) {
	var main Main
	var prompts Prompts
	view := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 3, Revision: 7}, Draft: conversationmodel.DraftView{Revision: 4, Available: true, Draft: composer.Draft{Text: "draft"}}}
	main.Update(view)
	transcript := activityui.NewView()
	edit := main.ComposerKey("x", &prompts, transcript)
	if edit.Editor.Version != view.Version || edit.Editor.Target.Revision != 4 || edit.Editor.Action.Kind != composer.InsertAction || edit.Editor.Action.Text != "x" {
		t.Fatalf("editor intent: %+v", edit)
	}
	command := main.ComposerKey("\t", &prompts, transcript)
	if command.Command.Operation != sessionpresentation.QueueComposer || command.Command.Version != view.Version || command.Command.Target != edit.Editor.Target {
		t.Fatalf("composer intent: %+v", command)
	}
	call := &prompt.Call{ID: "secret", Thread: "main", Questions: []prompt.Question{{IsSecret: true}}}
	var questions prompt.Questions
	questions.Add(call, false)
	questions.Open(call)
	view.Questions = questions.View("main")
	view.PromptDraft = conversationmodel.PromptDraftView{Call: "secret", Question: view.Questions.Call.Questions[0].ID}
	view.Draft = conversationmodel.DraftView{}
	main.Update(view)
	secret := main.ComposerKey("private", &prompts, transcript)
	if secret.Editor.Action.Kind != composer.NoAction || secret.Editor.Action.Text != "" || secret.Command.Operation != sessionpresentation.NoComposerOperation || !secret.Changed || prompts.Secret.Text != "private" {
		t.Fatal("secret text crossed the terminal editor boundary")
	}
	main.ComposerKey("\x1a", &prompts, transcript)
	if prompts.Secret.Text != "" {
		t.Fatal("secret undo did not stay local")
	}
}
