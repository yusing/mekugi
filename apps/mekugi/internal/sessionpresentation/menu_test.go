package sessionpresentation_test

import (
	"errors"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"testing"
)

func TestMenuPublicationAndChoiceAdmission(t *testing.T) {
	c := newStatusConsumer()
	source := &sessionpresentation.MenuView{ID: 1, Kind: sessionpresentation.MenuCopy, Choices: []completion.Choice{{ID: 1, Name: "Code block"}, {ID: 2, Name: "Code block"}}}
	publish := func() sessionpresentation.View {
		return c.model().Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, Menu: source}, sessionpresentation.Caption{})
	}
	v := publish()
	action := sessionpresentation.MenuAction{Version: v.Version, Menu: 1, Kind: sessionpresentation.MenuAccept, Choice: v.Menu.Choices[1].Identity()}
	if err := c.model().AdmitMenu(action); err != nil {
		t.Fatal(err)
	}
	v.Menu.Choices[1].Name = "reader mutation"
	source.Choices[0].Name = "source mutation"
	if c.model().View().Menu.Choices[0].Name != "Code block" || c.model().View().Menu.Choices[1].Name != "Code block" {
		t.Fatal("menu aliases mutable choices")
	}
	action.Choice.ID = 3
	if !errors.Is(c.model().AdmitMenu(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("unpublished duplicate-label choice admitted")
	}
	action.Choice.ID = 2
	source.ID++
	publish()
	if !errors.Is(c.model().AdmitMenu(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("replacement menu admitted old choice")
	}
}

func TestCompletionChoiceHonorsOrdinaryInputAdmission(t *testing.T) {
	c := newStatusConsumer()
	source := &sessionpresentation.MenuView{ID: 1, Choices: []completion.Choice{{Name: "file", Path: "file"}}}
	publish := func() sessionpresentation.View {
		return c.model().Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, Menu: source}, sessionpresentation.Caption{})
	}
	v := publish()
	if err := c.model().Display(sessionpresentation.DisplayIntent{Version: v.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}); err != nil {
		t.Fatal(err)
	}
	v = publish()
	action := sessionpresentation.MenuAction{Version: v.Version, Menu: 1, Kind: sessionpresentation.MenuAccept, Choice: source.Choices[0].Identity()}
	if !errors.Is(c.model().AdmitMenu(action), sessionpresentation.ErrInputUnavailable) {
		t.Fatal("child display admitted an ordinary draft completion")
	}
	action.Thread = v.Main.ID
	if err := c.model().AdmitMenu(action); err != nil || c.model().View().Displayed.ID != "child" {
		t.Fatalf("original Main completion did not survive navigation: %v", err)
	}
	action.Thread = "missing-side"
	if !errors.Is(c.model().AdmitMenu(action), sessionpresentation.ErrInputUnavailable) {
		t.Fatal("completion admitted a retired editor owner")
	}
}

func TestRewindAndArchiveMenusRejectReplay(t *testing.T) {
	for _, kind := range []sessionpresentation.MenuKind{sessionpresentation.MenuRewind, sessionpresentation.MenuRewindEdits, sessionpresentation.MenuArchive} {
		c := newStatusConsumer()
		c.model().Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, true)
		source := &sessionpresentation.MenuView{ID: 1, Kind: kind, Choices: []completion.Choice{{Name: "target"}}}
		v := c.model().Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, Menu: source}, sessionpresentation.Caption{})
		action := sessionpresentation.MenuAction{Version: v.Version, Menu: 1, Kind: sessionpresentation.MenuAccept, Choice: source.Choices[0].Identity()}
		if !errors.Is(c.model().AdmitMenu(action), sessionpresentation.ErrReplayIntent) {
			t.Fatal("replay admitted host operation", kind)
		}
	}
}
