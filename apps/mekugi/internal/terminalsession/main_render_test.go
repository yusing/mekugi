package terminalsession

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestMainFrameReturnsOutputDemands(t *testing.T) {
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	main.Update(sessionpresentation.View{Main: thread, Displayed: thread})
	view := activityui.NewView()
	view.Expansion = 2
	view.Update(&activitypresentation.Publication{Entries: []activitypresentation.Entry{{
		Facts:  activitypresentation.Facts{Seq: 7, Agent: "Main", Kind: "tool", Text: "command", Outputs: map[int]int{1: 3}},
		Values: []activitypresentation.Value{{Content: feed.Content{Kind: "op", Verb: "Run", Label: "printf retained"}, Source: 7, Output: 1, Exited: true, Tail: []string{"tail"}, TailOmitted: 1}},
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
