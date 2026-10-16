package terminalview

import (
	"testing"

	"github.com/yusing/mekugi/modules/conversation/completion"
)

func TestPickerKeysPreserveEditorAndEffectIntents(t *testing.T) {
	p := completion.State{Open: true, Target: completion.Target{Kind: '@', Query: "file"}}
	if PickerKey(&p, "x") != PickerPass || PickerKey(&p, "\r") != PickerPass || p.Open || p.Dismissed != p.Target {
		t.Fatal("unmatched file Enter must return to the editor and dismiss")
	}
	p.Open, p.Target.Kind = true, '$'
	if PickerKey(&p, "\r") != PickerHandled {
		t.Fatal("unmatched skill Enter must stay consumed")
	}
	p.Open, p.Choices = true, []completion.Choice{{Name: "one"}, {Name: "two"}}
	if PickerKey(&p, "\x1b[A") != PickerHandled || p.Selected != 1 || PickerKey(&p, "\t") != PickerAccept {
		t.Fatal("selection or accept intent changed")
	}
	if PickerKey(&p, "\x1b") != PickerDismiss || p.Open || p.Dismissed != p.Target {
		t.Fatal("dismiss must request cancellation and retain its target")
	}
	if SkillsKey(&p, true, "2") != PickerChooseSkills || p.Selected != 1 || SkillsKey(&p, false, " ") != PickerToggleSkill || SkillsKey(&p, false, "\x03") != PickerCloseSkills {
		t.Fatal("skill effects were not returned")
	}
}
