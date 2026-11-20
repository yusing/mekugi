package terminalsession

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func TestMenuKeysReturnTargetedEffectsWithoutMutatingPublication(t *testing.T) {
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Menu: &sessionpresentation.MenuView{ID: 1, Target: completion.Target{Kind: '@'}}}
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

func TestMenuCommandRefreshPreservesSelectedIdentity(t *testing.T) {
	v := sessionpresentation.View{Menu: &sessionpresentation.MenuView{
		ID: 1, Kind: sessionpresentation.MenuCompletion, Target: completion.Target{Kind: '/', Query: "res"},
		Choices: []completion.Choice{{Name: "/resume"}, {Name: "/reasoning"}},
	}}
	var m Menu
	m.Update(v)
	m.Key("\x1b[B")
	refreshed := *v.Menu
	refreshed.Choices = []completion.Choice{{Name: "/reasoning"}, {Name: "/resume"}}
	v.Menu = &refreshed
	v.Version.Revision++
	m.Update(v)
	if result := m.Key("\r"); result.Action.Choice.Name != "/reasoning" || !result.Submit {
		t.Fatalf("refresh changed the selected command: %+v", result)
	}
}

func TestRewindDialogInputIsolation(t *testing.T) {
	main := &Main{}
	main.Update(sessionpresentation.View{Menu: &sessionpresentation.MenuView{ID: 1, Kind: sessionpresentation.MenuRewind, Dialog: true,
		Choices: []completion.Choice{{Name: "first"}, {Name: "second"}}}})
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
	var painter terminalui.Painter
	main.Menu.Render(&painter, "", 60, 10)
	main.Menu.Rect = Rect{5, 4, 60, 10}
	for _, test := range []struct {
		x, y int
		kind sessionpresentation.MenuActionKind
	}{
		{8, 4 + main.Menu.RowStart + 1, sessionpresentation.MenuAccept},
		{0, 0, sessionpresentation.MenuDismiss},
		{57, 4, sessionpresentation.MenuDismiss}, // The heading's close control.
		{56, 4, sessionpresentation.MenuNoAction},
	} {
		in.mouse(&state, Mouse{X: test.x, Y: test.y}, func(effect InputEffect) bool {
			if effect.Menu == nil || effect.Menu.Action.Kind != test.kind || test.kind == sessionpresentation.MenuAccept && effect.Menu.Action.Choice.Name != "second" {
				t.Fatalf("pointer %+v: %+v", test, effect)
			}
			return false
		})
	}
}

func TestMenuDestructiveChoiceNeedsConfirmation(t *testing.T) {
	view := func(id uint64) sessionpresentation.View {
		return sessionpresentation.View{Menu: &sessionpresentation.MenuView{ID: id, Kind: sessionpresentation.MenuArchive, Title: "Archive this session?",
			Choices: []completion.Choice{{ID: 1, Name: "No, don't archive"}, {ID: 2, Name: "Yes, archive", Danger: true}}}}
	}
	var m Menu
	var painter terminalui.Painter
	m.Update(view(1))
	height := m.Height(60)
	m.Render(&painter, "", 60, height)
	m.Rect = Rect{0, 0, 60, height}
	if m.Selected != 0 {
		t.Fatal("the destructive choice was preselected")
	}
	row := m.RowStart + 1
	if r := m.Mouse(0, 4, row); !r.Handled || r.Action.Kind != sessionpresentation.MenuNoAction || m.Selected != 1 {
		t.Fatalf("one click accepted a destructive choice: %+v", r)
	}
	if r := m.Mouse(0, 4, row); r.Action.Kind != sessionpresentation.MenuAccept || r.Action.Choice.ID != 2 {
		t.Fatalf("second click did not confirm: %+v", r)
	}
	if r := m.Mouse(0, 4, row-1); r.Action.Kind != sessionpresentation.MenuAccept || r.Action.Choice.ID != 1 {
		t.Fatalf("one click did not accept the safe choice: %+v", r)
	}
	m.Update(view(2))
	m.Key("\x1b[B")
	if r := m.Key("\r"); r.Action.Kind != sessionpresentation.MenuAccept || r.Action.Choice.ID != 2 {
		t.Fatalf("Enter did not confirm the selected destructive choice: %+v", r)
	}
}
