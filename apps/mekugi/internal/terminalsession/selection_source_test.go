package terminalsession

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestFrozenSelectionRequestsUpdatedOutput(t *testing.T) {
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread, Version: sessionpresentation.Version{Attachment: 1}})
	view := activityui.NewView()
	view.Expansion = 2
	publication := activitypresentation.Publication{Entries: []activitypresentation.Entry{{
		Facts:  activitypresentation.Facts{Seq: 7, Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 2}},
		Values: []activitypresentation.Value{{Content: feed.Content{Kind: "op", Verb: "Run", Label: "printf retained"}, Source: 7, Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}},
	}, {
		Facts:  activitypresentation.Facts{Seq: 8, Agent: "Main", Kind: "text", Text: strings.Repeat("recent line\n", 12)},
		Values: []activitypresentation.Value{{Content: feed.Content{Kind: "text", Body: strings.Repeat("recent line\n", 12)}, Source: 8}},
	}}}
	view.Update(publication, activityui.Display{Caller: "/root"})
	painted := main.Render(view, &view.Painter, &prompts, MainOptions{Now: time.Unix(100, 0)}, 60, 8, 0)
	for _, demand := range painted.Demands {
		view.Hydrate([]activitypresentation.OutputResult{{Demand: demand, View: feed.OutputView{Lines: []string{"old retained line", "tail"}}}})
	}
	painted = main.Render(view, &view.Painter, &prompts, MainOptions{Now: time.Unix(100, 0)}, 60, 8, 0)
	if len(painted.Demands) != 0 || view.FeedLines <= view.FeedRows {
		t.Fatalf("fixture did not leave older output offscreen: demands=%v lines=%d rows=%d", painted.Demands, view.FeedLines, view.FeedRows)
	}
	var frame Frame
	if err := frame.Write(io.Discard, RenderedFrame{Rows: painted.Rows}, 60, false); err != nil {
		t.Fatal(err)
	}
	// Source publication can advance between delivery and the next pointer event.
	publication.Entries = slices.Clone(publication.Entries)
	publication.Entries[0].Revision++
	publication.Entries[0].Outputs = map[int]int{1: 3}
	view.Update(publication, activityui.Display{Caller: "/root"})
	in := FrameInput{Main: &main, Transcript: view, Shell: &Shell{Layout: Layout{Main: Rect{0, 0, 60, 8}}}, Width: 60, Height: 8}
	mouse := Mouse{X: view.FeedLeft - 1, Y: view.FeedTop - 1}
	result := frame.SelectionMouse(in, nil, mouse)
	if !result.Handled || len(result.Demands) == 0 {
		t.Fatal("frozen capture did not request its offscreen output")
	}
	results := make([]activitypresentation.OutputResult, len(result.Demands))
	for i, demand := range result.Demands {
		if demand.Source != 7 || demand.Output != 1 || demand.Version != 3 {
			t.Fatalf("wrong retained target: %+v", demand)
		}
		results[i] = activitypresentation.OutputResult{Demand: demand, View: feed.OutputView{Lines: []string{"retained first line", "tail"}}}
	}
	view.Hydrate(results)
	result = frame.SelectionMouse(in, nil, mouse)
	if len(result.Demands) != 0 || result.Selection == nil || !strings.Contains(strings.Join(result.Selection.Rows, "\n"), "retained first line") {
		t.Fatalf("frozen capture lost hydrated content: %+v", result)
	}
}

func TestSelectionTargetRetainsPressedIdentity(t *testing.T) {
	var main Main
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	v := sessionpresentation.View{Main: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: "/original"}, Version: sessionpresentation.Version{Attachment: 7, Revision: 1}}
	main.Update(v)
	view := activityui.NewView()
	view.FeedLeft, view.FeedTop, view.FeedRight, view.FeedRows = 1, 1, 20, 1
	view.FeedQuestions = []uint64{11}
	var frame Frame
	if err := frame.Write(io.Discard, RenderedFrame{Rows: []string{"question", ""}, Source: SourceContext{Attachment: 7, Thread: "main", Workspace: "/original"}}, 20, false); err != nil {
		t.Fatal(err)
	}
	in := FrameInput{Main: &main, Transcript: view, Shell: &Shell{Layout: Layout{Main: Rect{0, 0, 20, 1}}}, Width: 20, Height: 2}
	v.Version = sessionpresentation.Version{Attachment: 8, Revision: 5}
	v.Info.Workspace = "/replacement"
	main.Update(v)
	pressed := frame.SelectionMouse(in, nil, Mouse{X: 1})
	if pressed.Selection == nil {
		t.Fatal("press did not retain its target")
	}
	view.FeedQuestions = []uint64{22}
	released := frame.SelectionMouse(in, pressed.Selection, Mouse{X: 1, Release: true})
	if !released.Handled || released.Selection != nil || released.Target.Version != (sessionpresentation.Version{Attachment: 7, Revision: 5}) || released.Target.Entry != 11 || released.Target.Workspace != "/original" || released.Target.Thread != "main" {
		t.Fatalf("press retargeted: %+v", released)
	}
}
