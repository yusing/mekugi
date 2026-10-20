package terminalsession

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestOuterInputContinuesAfterDisplayPublication(t *testing.T) {
	var main Main
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 4, Revision: 1}, Main: sessionpresentation.Thread{ID: "main"}, Displayed: sessionpresentation.Thread{ID: "child"}, ReadOnly: sessionpresentation.ChildDisplayed}
	main.Update(v)
	state := Interaction{Prefix: true}
	in := FrameInput{Main: &main, Shell: &Shell{}}
	steps := 0
	for effect := range in.Dispatch(&state, Input{Kind: KeyInput, Text: "q"}) {
		steps++
		switch steps {
		case 1:
			if effect.Display == nil || effect.Display.Version != v.Version || effect.Display.Thread != v.Main {
				t.Fatalf("missing original display target: %+v", effect)
			}
			v.Version.Revision++
			v.Displayed, v.ReadOnly = v.Main, sessionpresentation.Editable
			main.Update(v)
		case 2:
			if effect.PendingPrompt == nil || effect.PendingPrompt.Version != v.Version || !effect.PendingPrompt.ApprovalsFirst {
				t.Fatalf("prompt opening did not use settled display: %+v", effect)
			}
		default:
			t.Fatal("input escaped the pending-prompt action")
		}
	}
	if steps != 2 || state.Prefix {
		t.Fatal("prefix or continuation remained pending")
	}
}

func TestOuterReferenceRetainsSelectionOnRejectedEffect(t *testing.T) {
	var main Main
	main.Update(sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 3}})
	selection := NewSelection(Surface{Rect: Rect{0, 0, 8, 1}, Rows: []string{"retained"}}, 0, 0)
	selection.Move(7, 0, true)
	state := Interaction{Selection: &SourceSelection{Selection: selection, Mention: "message"}}
	in := FrameInput{Main: &main, Shell: &Shell{}}
	for effect := range in.Send(&state, "r") {
		if effect.Reference == nil || effect.Reference.Text != "retained" {
			t.Fatalf("wrong reference: %+v", effect)
		}
		break // Application rejects admission, consuming the input.
	}
	if state.Selection == nil || state.Selection.Text() != "retained" {
		t.Fatal("rejected Reference lost its frozen source")
	}
}

func TestOuterEscapeUsesPublishedInterruptionAdmission(t *testing.T) {
	var main Main
	v := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 3}, InterruptAvailable: true}
	main.Update(v)
	view := activityui.NewView()
	in := FrameInput{Main: &main, Prompts: &Prompts{}, Transcript: view, Shell: &Shell{}}
	interrupts := 0
	for effect := range in.Send(&Interaction{}, "\x1b") {
		if effect.Interrupt != nil {
			interrupts++
			if effect.Interrupt.Version != v.Version {
				t.Fatal("interrupt target lost its publication")
			}
		}
	}
	if interrupts != 1 {
		t.Fatal("empty visible queues suppressed admitted interruption")
	}
}
