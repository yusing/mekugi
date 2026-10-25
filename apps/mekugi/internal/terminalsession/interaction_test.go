package terminalsession

import (
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestPrefixResizeRepeat(t *testing.T) {
	var state Interaction
	in := FrameInput{Main: &Main{}, Transcript: activityui.NewView(), Shell: &Shell{Split: 60}, Width: 140, Now: time.Unix(100, 0)}
	// Feed the same fragmented escape sequences as live terminal input.
	feed := func(keys string, wantSplit int, wantKeys string) {
		t.Helper()
		var gotKeys string
		for _, b := range []byte(keys) {
			for effect := range in.Dispatch(&state, state.Decoder.Feed(b, in.Now)) {
				if effect.Main != nil {
					gotKeys += effect.Main.key
				}
			}
		}
		if in.Shell.Split != wantSplit || gotKeys != wantKeys {
			t.Fatalf("input %q: split=%d, keys=%q; want %d, %q", keys, in.Shell.Split, gotKeys, wantSplit, wantKeys)
		}
	}
	feed("\x02\x1b[C", 63, "")
	feed("\x1b]10;rgb:ff/ff/ff\a\x1b[I", 63, "") // Terminal replies leave repeats intact.
	in.Now = in.Now.Add(400 * time.Millisecond)
	feed("\x1b[C", 66, "")
	in.Now = in.Now.Add(400 * time.Millisecond) // Repeat renews the deadline.
	feed("\x1b[D", 63, "")
	in.Now = in.Now.Add(500 * time.Millisecond)
	feed("\x1b[D", 63, "\x1b[D") // Expired arrows retain their normal action.
	feed("\x02\x1b[Cx\x1b[D", 66, "x\x1b[D")
	feed("\x02\x1b[C\x1b[A\x1b[D", 69, "\x1b[A\x1b[D")
	feed("\x02h\x1b[C", 66, "\x1b[C") // Existing non-arrow prefix commands are single-shot.
	feed("\x02\x1b[C\x02\x02\x1b[D", 69, "\x02\x1b[D")
	feed("\x02\x1b[5~\x1b[C", 69, "\x1b[C")
}

func TestDecoderInterruptedReplyDispatch(t *testing.T) {
	for _, reset := range []string{"\x03", "\x18", "\x1a", "\x1b[D"} {
		var state Interaction
		in := FrameInput{Main: &Main{}, Transcript: activityui.NewView(), Shell: &Shell{}, Now: time.Unix(1, 0)}
		keys := ""
		for _, b := range []byte("\x1b]11;rgb:ffff" + reset + "text") {
			for effect := range in.Dispatch(&state, state.Decoder.Feed(b, in.Now)) {
				if effect.Main != nil {
					keys += effect.Main.key
				}
			}
		}
		if keys != reset+"text" || state.Decoder.Pending() || state.Decoder.Pasting() {
			t.Fatalf("reset %q: dispatched %q", reset, keys)
		}
	}
}

func TestPrefixResizeRepeatCancellation(t *testing.T) {
	for _, stop := range []string{"paste", "pointer", "focus loss", "dialog", "escape", "pane"} {
		t.Run(stop, func(t *testing.T) {
			var state Interaction
			in := FrameInput{Main: &Main{}, Prompts: &Prompts{}, Transcript: activityui.NewView(), Shell: &Shell{Split: 60}, Width: 140, Now: time.Unix(100, 0)}
			dispatch := func(event Input) {
				for range in.Dispatch(&state, event) {
				}
			}
			dispatch(Input{KeyInput, "\x02"})
			dispatch(Input{KeyInput, "\x1b[C"})
			switch stop {
			case "paste":
				dispatch(Input{Kind: PasteStart})
			case "pointer":
				dispatch(Input{Kind: MouseInput})
			case "focus loss":
				dispatch(Input{FocusInput, "\x1b[O"})
			case "dialog":
				in.Dialog = &ContentDialog{}
				dispatch(Input{})
				in.Dialog = nil
			case "escape":
				for range in.Send(&state, "\x1b") {
				}
			case "pane":
				for range in.SelectPane(&state, 0) {
				}
			}
			keys := 0
			for effect := range in.Dispatch(&state, Input{KeyInput, "\x1b[C"}) {
				if effect.Main != nil && effect.Main.key == "\x1b[C" {
					keys++
				}
			}
			if in.Shell.Split != 63 || keys != 1 {
				t.Fatalf("resize window survived %s: split=%d, normal arrows=%d", stop, in.Shell.Split, keys)
			}
		})
	}
}

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
