package terminalsession

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/conversation/presets"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestPresetConfirmKeys(t *testing.T) {
	var menu Menu
	menu.Update(sessionpresentation.View{Menu: &sessionpresentation.MenuView{ID: 2, Kind: "preset-confirm", Choices: []completion.Choice{{Name: "confirm"}}}})
	if menu.Key("\r").Action.Kind != sessionpresentation.MenuNoAction {
		t.Fatal("held Enter confirmed a replacement")
	}
	if menu.Key("y").Action.Choice.Name != "confirm" {
		t.Fatal("explicit confirmation missing")
	}
}

// Slot 1 is a long single line, which must not widen the selection band.
var presetSlots = [10]presets.Slot{
	0: {Input: "/model example --reasoning=high --speed=fast"},
	1: {Input: "Inspect the pending changes for correctness, explain the most important risks, and suggest focused regression coverage."},
	2: {Input: "Review the current changes\nthen summarize the risks."},
	3: {Input: "/review"},
}

// presetMenu publishes a flow the way the composer picker does.
func presetMenu(id uint64, f presets.Flow) *sessionpresentation.MenuView {
	menu := &sessionpresentation.MenuView{ID: id, Kind: sessionpresentation.MenuKind(f.Stage), Title: f.Title(), Explanation: f.Explanation()}
	menu.Choices, menu.Problem, menu.Loading, menu.Query = f.Choices(), f.Problem, f.Pending, f.Query()
	return menu
}

// renderPreset renders at the menu's own height unless height is positive,
// after selecting the choice at selected.
func renderPreset(menu *Menu, view *sessionpresentation.MenuView, selected, width, height int) []string {
	var painter terminalui.Painter
	menu.Update(sessionpresentation.View{Menu: view})
	menu.Selected = selected
	if height <= 0 {
		height = menu.Height(width)
	}
	return menu.Render(&painter, SelectionStyle(&painter), width, height)
}

func TestUISnapshotPresets(t *testing.T) {
	const plain = "plain after presets"
	replace := presets.Flow{Stage: presets.Confirm, Slot: 1, Mode: "edit", Loaded: true, Slots: presetSlots, Input: "Review changes\nand explain tradeoffs."}
	saving := replace
	saving.Pending = true
	longSeed := "Review the staged diff.\nList correctness risks first.\nThen performance.\nThen naming.\nThen tests.\nThen docs.\nKeep it short.\nCite files."
	for _, tc := range []struct {
		name  string
		width int
		menus []*sessionpresentation.MenuView
	}{
		{"presets", 80, []*sessionpresentation.MenuView{
			presetMenu(1, presets.Flow{Stage: presets.Quick, Slot: -1, Loaded: true, Slots: presetSlots}),
			presetMenu(2, presets.Flow{Stage: presets.Slots, Mode: "edit", Loaded: true, Slots: presetSlots}),
			presetMenu(3, presets.Flow{Stage: presets.Quick, Slot: -1, Problem: "Read preset 0: permission denied"}),
			presetMenu(4, presets.Flow{Stage: presets.Quick, Slot: -1, Pending: true}),
		}},
		{"presets-edit", 60, []*sessionpresentation.MenuView{
			presetMenu(1, presets.Flow{Stage: presets.Edit, Slot: 2, Mode: "edit", Loaded: true, Slots: presetSlots, Input: presetSlots[2].Input}),
			presetMenu(2, presets.Flow{Stage: presets.Edit, Slot: -1, Loaded: true, Slots: presetSlots, Input: longSeed}),
			presetMenu(3, presets.Flow{Stage: presets.Edit, Slot: -1, Loaded: true, Slots: presetSlots, Problem: presets.Validate("").Error()}),
		}},
		{"presets-label", 60, []*sessionpresentation.MenuView{
			presetMenu(1, presets.Flow{Stage: presets.Label, Slot: -1, Loaded: true, Slots: presetSlots, Input: presetSlots[2].Input}),
			presetMenu(2, presets.Flow{Stage: presets.Label, Slot: 1, Mode: "rename", Loaded: true, Slots: presetSlots, Input: presetSlots[1].Input, Label: "Correctness review"}),
			presetMenu(3, presets.Flow{Stage: presets.Label, Slot: 3, Mode: "edit", Loaded: true, Slots: presetSlots, Input: presetSlots[3].Input, Label: "two\nlines", Problem: presets.ValidateLabel("two\nlines").Error()}),
		}},
		{"presets-confirm", 60, []*sessionpresentation.MenuView{
			presetMenu(1, replace),
			presetMenu(2, presets.Flow{Stage: presets.Confirm, Slot: 0, Mode: "delete", Loaded: true, Slots: presetSlots}),
			presetMenu(3, saving),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var menu Menu
			var rows []string
			for _, view := range tc.menus {
				rows = append(rows, renderPreset(&menu, view, 0, tc.width, 0)...)
			}
			uisnapshot.AssertTerminal(t, "testdata/snapshots/"+tc.name+".txt", append(rows, plain), tc.width)
		})
	}
	t.Run("presets-narrow", func(t *testing.T) {
		// A short terminal clamps the quick menu, which scrolls to keep the
		// selected action visible between its markers.
		var menu Menu
		rows := renderPreset(&menu, presetMenu(1, presets.Flow{Stage: presets.Quick, Slot: -1, Loaded: true, Slots: presetSlots}), 11, 36, 9)
		rows = append(rows, renderPreset(&menu, presetMenu(2, presets.Flow{Stage: presets.Edit, Slot: 2, Mode: "edit", Loaded: true, Slots: presetSlots, Input: presetSlots[2].Input}), 0, 36, 0)...)
		rows = append(rows, renderPreset(&menu, presetMenu(3, replace), 0, 36, 0)...)
		// A long label keeps its end and caret in view.
		rows = append(rows, renderPreset(&menu, presetMenu(4, presets.Flow{Stage: presets.Label, Slot: 1, Mode: "rename", Loaded: true, Slots: presetSlots, Label: "Correctness review with focused regression coverage"}), 0, 36, 0)...)
		uisnapshot.AssertTerminal(t, "testdata/snapshots/presets-narrow.txt", append(rows, plain), 36)
	})
}

func TestPresetLabelKeys(t *testing.T) {
	var menu Menu
	view := &sessionpresentation.MenuView{ID: 3, Kind: "preset-label", Choices: []completion.Choice{{Name: "continue", Display: "Save"}}}
	view.Query = "Revi"
	menu.Update(sessionpresentation.View{Menu: view})
	// A burst of keys arrives before the host echoes any of them. Slot digits
	// and action letters are label text here, not shortcuts.
	for _, step := range [][2]string{{"1", "Revi1"}, {"y", "Revi1y"}, {"é", "Revi1yé"}, {"\x7f", "Revi1y"}, {"\x7f", "Revi1"}} {
		if got := menu.Key(step[0]).Action; got.Kind != sessionpresentation.MenuSearch || got.Query != step[1] {
			t.Fatalf("key %q: %+v, want query %q", step[0], got, step[1])
		}
	}
	if got := menu.Key("\r").Action; got.Kind != sessionpresentation.MenuAccept || got.Choice.Name != "continue" || got.Query != "Revi1" {
		t.Fatalf("enter did not save the typed label: %+v", got)
	}
	if got := menu.Key("\x15").Action; got.Kind != sessionpresentation.MenuSearch || got.Query != "" {
		t.Fatalf("ctrl-u did not clear the label: %+v", got)
	}
	if got := menu.Key("\x1b").Action; got.Kind != sessionpresentation.MenuDismiss {
		t.Fatalf("escape did not cancel: %+v", got)
	}
}

func TestPresetRetryKeys(t *testing.T) {
	var menu Menu
	menu.Update(sessionpresentation.View{Menu: &sessionpresentation.MenuView{ID: 1, Kind: "presets", Problem: "Read preset 0: permission denied", Choices: []completion.Choice{{Name: "retry", Display: "Retry loading presets"}}}})
	for _, key := range []string{"r", "\r"} {
		if got := menu.Key(key); got.Action.Kind != sessionpresentation.MenuAccept || got.Action.Choice.Name != "retry" {
			t.Fatalf("retry %q: %+v", key, got)
		}
	}
}
