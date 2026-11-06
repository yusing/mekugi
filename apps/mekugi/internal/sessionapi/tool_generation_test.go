package sessionapi

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestToolGenerationReachesSharedRosterConsumers(t *testing.T) {
	at := time.Unix(100, 0)
	var observed sessioncontrol.Observations
	attached := sessionpresentation.Attachment{ToolInput: observed.ToolInput}
	attached.Start("root")
	main := &sessionpresentation.ActivitySource{Model: &attached.Activity().Main}
	children := &sessionpresentation.ActivitySource{Model: &attached.Activity().Children}
	model := new(sessionpresentation.Model)
	for _, thread := range []string{"root", "child"} {
		name := "/root"
		if thread == "child" {
			name += "/worker"
		}
		attached.RegisterThread(observation.Thread{ID: thread, Path: name})
		attached.Lifecycle(thread).TurnStarted(at)
		generation := new(sessioncontrol.Generation)
		observed.SetToolInput(generation, "", thread, sessioncontrol.GenerationItem{ID: "item"}, true)
		observed.IdentifyToolInput(generation, sessioncontrol.GenerationItem{ID: "item"}, "mcp__threadcontrol__spawn_agent", false, `{"task_name":"reviewer","message":"private payload`)
	}
	// Main's existing transcript summary must not overwrite its live generation.
	main.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "Older reasoning"}}})
	children.Apply(sessionpresentation.ActivityEvent{Agents: attached.Roster()})
	publication := children.Publication(model, main)
	projected := projectRoster(publication, "/root")
	view := terminalview.NewView()
	view.MainSummary = []terminalview.Block{{Kind: "summary", Label: "Older reasoning"}}
	for i, row := range publication.Roster {
		if row.Agent.Summary[0].Observed.Before(row.Agent.WorkSince) {
			t.Fatal("browser would hide current generation as preceding current work")
		}
		if projected.Agents[i].Activity == nil {
			t.Fatalf("missing browser activity for %s", row.Agent.Name)
		}
		values := projected.Agents[i].Activity.Values
		painted := ansi.Strip(view.AgentState(row.Agent, 90, at))
		current, _ := view.Current(row.Agent, at, 90)
		if len(values) != 1 || values[0].Body != "Spawning subagent `reviewer`" || !strings.Contains(painted, "Spawning subagent reviewer") {
			t.Fatalf("shared roster mismatch: API=%+v terminal=%q", values, painted)
		}
		if !strings.Contains(ansi.Strip(current), "Spawning subagent reviewer") {
			t.Fatalf("direct terminal summary replaced generation: %q", current)
		}
		if strings.Contains(painted, "private payload") {
			t.Fatal("roster leaked tool input")
		}
	}
	attached.Lifecycle("root").TurnCompleted(true, at)
	children.Apply(sessionpresentation.ActivityEvent{Agents: attached.Roster()})
	publication = children.Publication(model, main)
	if publication.Roster[0].Agent.Generating != "" || publication.Roster[1].Agent.Generating == "" {
		t.Fatal("turn completion did not isolate generation by thread")
	}
}
