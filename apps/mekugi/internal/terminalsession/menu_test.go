package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"testing"
)

func TestMenuKeysReturnTargetedEffectsWithoutMutatingPublication(t *testing.T) {
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Menu: &sessionpresentation.MenuView{ID: 1, View: completion.View{Target: completion.Target{Kind: '@'}}}}
	var m Menu
	m.Update(v)
	if result := m.Key("x"); result.Handled || result.Action.Kind != sessionpresentation.MenuNoAction {
		t.Fatal("ordinary text did not pass to editor")
	}
	if result := m.Key("\r"); result.Handled || result.Action.Kind != sessionpresentation.MenuDismiss || !m.Open() {
		t.Fatal("empty file Enter did not return dismissal and continue")
	}
	v.Menu.Target.Kind = '$'
	m.Update(v)
	if result := m.Key("\r"); !result.Handled {
		t.Fatal("empty skill Enter escaped")
	}
	v.Menu.Choices = []completion.Choice{{Name: "one"}, {Name: "two"}}
	m.Update(v)
	m.Key("\x1b[A")
	result := m.Key("\t")
	if m.Selected != 1 || result.Action.Kind != sessionpresentation.MenuAccept || result.Action.Choice.Name != "two" || result.Action.Version != v.Version {
		t.Fatalf("accept: %+v", result)
	}
	if result := m.Key("\x1b"); !result.Handled || result.Action.Kind != sessionpresentation.MenuDismiss || !m.Open() {
		t.Fatal("dismissal mutated published lifetime")
	}
	v.Menu.Kind = sessionpresentation.MenuSkills
	m.Update(v)
	if result := m.Key("2"); result.Action.Choice.Name != "two" {
		t.Fatal("numbered skill action lost identity")
	}
	v.Menu.Kind = sessionpresentation.MenuManageSkills
	m.Update(v)
	if result := m.Key(" "); result.Action.Kind != sessionpresentation.MenuAccept {
		t.Fatal("toggle did not return a choice action")
	}
	if result := m.Key("a"); result.Action.Kind != sessionpresentation.MenuSearch || result.Action.Query != "a" || v.Menu.Query != "" {
		t.Fatal("search mutated the semantic owner")
	}
}
