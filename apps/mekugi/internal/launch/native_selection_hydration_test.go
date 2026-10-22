package launch

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestNativeSelectionHydrationYieldsAndPreservesGesture(t *testing.T) {
	for _, ending := range []string{"reference", "display", "resize", "escape", "editing", "unicode", "replacement"} {
		t.Run(ending, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.testMainFrame(60, 12, 0)
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			terminal.Width, terminal.Height = 60, 12
			c := nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			publication := &activity.Publication{Entries: []activity.Entry{{
				Seq: 7, Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 2},
				Values: []activity.Value{{Kind: "op", Verb: "Run", Label: "printf retained", Source: 7, Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}},
			}, {
				Seq: 8, Agent: "Main", Kind: "text", Text: strings.Repeat("recent line\n", 40),
				Values: []activity.Value{{Kind: "text", Body: strings.Repeat("recent line\n", 40), Source: 8}},
			}}}
			event := sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: publication}}
			c.accept(event)
			terminal.Transcript.Expansion = 2
			for range 2 {
				painted, err := c.renderer.render(t.Context(), io.Discard, time.Now(), "test")
				if err != nil {
					t.Fatal(err)
				}
				for _, demand := range painted.Demands {
					terminal.Transcript.Hydrate([]activity.OutputResult{{Demand: demand, View: feed.OutputView{Lines: []string{"old output", "tail"}}}})
				}
			}
			publication.Entries[0].Revision++
			publication.Entries[0].Outputs = map[int]int{1: 3}
			event.Frame.Main = &activity.Publication{Entries: publication.Entries}
			c.accept(event)
			c.calls = nil
			x := terminal.Layout.Main.X + terminal.Transcript.FeedLeft
			y := terminal.Layout.Main.Y + terminal.Transcript.FeedTop
			c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dM", x, y)})
			if c.selection == nil || len(c.calls) != 1 || c.calls[0].operation.Output == nil {
				t.Fatalf("selection did not yield one acquisition: pending=%v calls=%d", c.selection != nil, len(c.calls))
			}
			call := c.calls[0]
			c.calls = nil
			c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<32;%d;%dM", x+6, y+1)})
			c.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dm", x+6, y+1)})
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "r"})
			switch ending {
			case "escape":
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			case "editing":
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "z"})
			case "unicode":
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "界"})
				if c.selection != nil || c.editor("main").Text != "界" {
					t.Fatal("Unicode editing waited for selection hydration")
				}
			case "display":
				event.Frame.View.Displayed = sessionpresentation.Thread{ID: "replacement"}
			case "resize":
				terminal.Width, terminal.Height = 80, 24
			case "replacement":
				event.Frame.View.Version.Attachment++
			}
			for _, demand := range call.operation.Output.Demands {
				event.Frame.MainOutput = append(event.Frame.MainOutput, activity.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{"retained output", "tail"}}})
			}
			c.accept(event)
			call.after(nil)
			c.drainInput()
			var reference *sessionpresentation.ReferenceIntent
			for _, call := range c.calls {
				if call.operation.Reference != nil {
					reference = call.operation.Reference
				}
			}
			if ending == "reference" || ending == "display" || ending == "resize" {
				if reference == nil || !strings.Contains(reference.Text, "recent") {
					t.Fatalf("lost frozen reference: %+v", reference)
				}
				if reference.Displayed.ID == "replacement" {
					t.Fatal("reference followed the new display")
				}
			} else if reference != nil || terminal.Selection != nil || c.selection != nil {
				t.Fatal("retired capture resurrected a selection")
			}
			if ending == "editing" && c.editor("main").Text != "z" {
				t.Fatal("hydration swallowed ordinary editing")
			}
		})
	}
}
