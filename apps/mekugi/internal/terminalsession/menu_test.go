package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
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

func TestRewindDialogInputIsolation(t *testing.T) {
	main := &Main{}
	main.Update(sessionpresentation.View{Menu: &sessionpresentation.MenuView{ID: 1, Kind: sessionpresentation.MenuRewind, Dialog: true,
		View: completion.View{Choices: []completion.Choice{{Name: "first"}, {Name: "second"}}}}})
	main.Menu.Rect = Rect{5, 4, 60, 10}
	main.Menu.RowStart, main.Menu.RowCount = 5, 2
	in := FrameInput{Main: main, Shell: &Shell{}, Transcript: activityui.NewView()}
	var state Interaction
	for _, event := range []Input{{Kind: PasteStart}, {Kind: PasteInput, Text: "ignored"}, {Kind: PasteEnd}, {Kind: KeyInput, Text: "\x02"}, {Kind: KeyInput, Text: "2"}} {
		for effect := range in.Dispatch(&state, event) {
			if effect.Menu == nil || !effect.Menu.Handled || effect.Menu.Action.Kind != sessionpresentation.MenuNoAction {
				t.Fatalf("dialog input escaped: %+v", effect)
			}
		}
	}
	if state.Prefix || in.Shell.Focus != 0 {
		t.Fatal("dialog allowed pane switching")
	}
	for _, test := range []struct {
		x, y int
		kind sessionpresentation.MenuActionKind
	}{
		{8, 10, sessionpresentation.MenuAccept},
		{0, 0, sessionpresentation.MenuDismiss},
		{61, 4, sessionpresentation.MenuDismiss},
	} {
		in.mouse(&state, Mouse{X: test.x, Y: test.y}, func(effect InputEffect) bool {
			if effect.Menu == nil || effect.Menu.Action.Kind != test.kind || test.kind == sessionpresentation.MenuAccept && effect.Menu.Action.Choice.Name != "second" {
				t.Fatalf("pointer %+v: %+v", test, effect)
			}
			return false
		})
	}
}
