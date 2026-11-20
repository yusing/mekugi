package terminalsession

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestMainFrameReturnsOutputDemands(t *testing.T) {
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread})
	view := activityui.NewView()
	view.Expansion = 2
	view.Update(&activitypresentation.Publication{Entries: []activitypresentation.Entry{{
		Seq: 7, Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 3},
		Values: []activitypresentation.Value{{Kind: "op", Verb: "Run", Label: "printf retained", Source: 7, Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}},
	}}}, activityui.Display{Caller: "/root"})
	options := MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}
	frame := main.Render(view, &view.Painter, &prompts, options, 100, 24, 0)
	if len(frame.Demands) == 0 {
		t.Fatal("Main did not return required output demands")
	}
	results := make([]activitypresentation.OutputResult, len(frame.Demands))
	for i, demand := range frame.Demands {
		if demand.Source != 7 || demand.Output != 1 || demand.Version != 3 {
			t.Fatalf("output identity = %+v", demand)
		}
		results[i] = activitypresentation.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{"retained first line", "tail"}}}
	}
	view.Hydrate(results)
	frame = main.Render(view, &view.Painter, &prompts, options, 100, 24, 0)
	if len(frame.Demands) != 0 || !strings.Contains(strings.Join(frame.Rows, "\n"), "retained first line") {
		t.Fatalf("Main did not consume detached hydration: %+v\n%s", frame.Demands, strings.Join(frame.Rows, "\n"))
	}
}

func TestUISnapshotPreviewWelcome(t *testing.T) {
	saved := previewWelcome
	previewWelcome = "## Preview the terminal UI\n\n" +
		"The preview runs locally and needs no credentials. Its workspace and " +
		"session data are discarded when it exits.\n\n" +
		"- Submit `demo` to start. Use **Ctrl+B, 2** to open Saved Diff.\n" +
		"- Use `/quit` to exit."
	t.Cleanup(func() { previewWelcome = saved })

	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread})
	transcript := activityui.NewView()
	transcript.Painter.Theme = diffrender.DarkTheme
	options := MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}
	frame := main.Render(transcript, &transcript.Painter, &prompts, options, 96, 14, 0)
	uisnapshot.Assert(t, "testdata/snapshots/preview-welcome.txt", strings.Join(frame.Rows, "\n"))
}
