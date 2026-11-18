package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestPreparedChildStartupPreservesObservationAudience(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.audience = sessioncontrol.NewAudience(nil)
	u.Startup.Thread = "child"
	u.nativeAgent = &binding.ThreadInfo{ID: "child", ParentThreadID: "main", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker"}}}`)}
	u.audience.ObserveAdmitted("child", "main", "/root/worker", true)
	u.audience.AttachEndpoint("child")
	appServerTestRequest(t, u, "1", "thread/resume")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"child","cwd":"/child","turns":[]}}}`)
	if !u.audience.Observe("child", "main", "/root/worker", true) {
		t.Fatal("startup conflicted with admitted child identity")
	}
	u.audience.ObservePrepared("child", activity.PreparedContent{ID: "exec", Text: "model-visible input"})
	if content, ok := u.audience.Prepared("child", "child", "exec"); !ok || content.Text != "model-visible input" {
		t.Fatal("child runtime lost model-visible tool input")
	}
	u.audience.BeginResponse("child", true, time.Unix(1, 0))
	if starts := u.audience.TakeStarts("child"); len(starts) != 1 {
		t.Fatalf("child runtime lost generation tracking: %+v", starts)
	}
}

func TestCanonicalChildCommandInThreadAndRoster(t *testing.T) {
	workspace := t.TempDir()
	child, _ := newAppServerTestUI()
	child.ctx = t.Context()
	child.Startup.Thread = "child"
	child.nativeAgent = &binding.ThreadInfo{ID: "child", ParentThreadID: "main", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker"}}}`)}
	appServerTestRequest(t, child, "1", "thread/resume")
	appServerTestMessage(t, child, `{"id":1,"result":{"thread":{"id":"child","turns":[]}}}`)
	parent := newAppServerSessionTestUI(t, workspace)
	appServerTestNotify(t, parent, "thread/started", map[string]any{"thread": child.nativeAgent})
	for _, ui := range []*appServerUI{child, parent} {
		appServerTestNotify(t, ui, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "turn"}})
	}
	notify := func(ui *appServerUI, phase string, item binding.ThreadItem) {
		appServerTestNotify(t, ui, phase, map[string]any{"threadId": "child", "turnId": "turn", "item": item})
	}
	command := binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "fixture-command LOCAL_CASE", Status: "inProgress"}
	first := binding.ThreadItem{ID: "before", Type: "reasoning", Summary: []string{"**Local reasoning before**"}}
	last := binding.ThreadItem{ID: "after", Type: "reasoning", Summary: []string{"**Local reasoning after**"}}
	notify(child, "item/completed", first)
	child.controlObservations = new(sessioncontrol.Observations)
	child.attached.session.ToolInput = child.controlObservations.ToolInput
	var generation sessioncontrol.Generation
	call := sessioncontrol.GenerationItem{ID: "exec"}
	child.controlObservations.SetToolInput(&generation, workspace, "child", call, true)
	child.controlObservations.IdentifyToolInput(&generation, call, "exec", true, `await tools.exec_command({cmd: "fixture-command`)
	notify(child, "item/started", command)
	roster := func() activity.Agent {
		t.Helper()
		publication := child.attached.childSource.Publication(child.attached.presentation, child.attached.source)
		for _, row := range publication.Roster {
			if row.Agent.Thread == "child" && row.Agent.Name == "/root/worker" && row.Agent.Responding {
				return row.Agent
			}
		}
		t.Fatalf("active canonical child missing from roster: %+v", publication.Roster)
		return activity.Agent{}
	}
	if agent := roster(); len(agent.Summary) != 1 || agent.Generating != "Executing a command" || agent.Summary[0].Kind != "progress" {
		t.Fatalf("transient tool generation lost its roster label: %+v", agent)
	}
	child.controlObservations.EndGeneration(&generation)
	command.Status, command.ExitCode, command.AggregatedOutput = "failed", new(7), new("LOCAL_CASE\n")
	notify(child, "item/completed", command)
	notify(parent, "item/completed", command)
	if agent := roster(); len(agent.Summary) != 1 || agent.Generating != "" || agent.Summary[0].Kind != "tool" || !strings.Contains(agent.Summary[0].Text, "LOCAL_CASE") {
		t.Fatalf("active child's roster did not show its completed command: %+v", agent)
	}
	notify(child, "item/completed", last)
	assertTranscript := func(ui *appServerUI) {
		t.Helper()
		ui.shell.Transcript.Conversation = true
		rollCommandOutput(ui)
		transcript := ansi.Strip(strings.Join(ui.testActivity(ui.shell.Transcript).RenderFeed(100, 28).Lines, "\n"))
		before, cmd, after := strings.Index(transcript, "Local reasoning before"), strings.Index(transcript, "LOCAL_CASE"), strings.Index(transcript, "Local reasoning after")
		if before < 0 || cmd <= before || after <= cmd || !strings.Contains(transcript, "exit 7") {
			var admitted []string
			for _, record := range ui.attached.source.Entries {
				admitted = append(admitted, record.Source.Agent+":"+record.Source.Kind+":"+record.Source.Text)
			}
			t.Fatalf("child Thread pane lost command order or outcome: admitted=%q\n%s", admitted, transcript)
		}
	}
	assertTranscript(child)
	if strings.Contains(strings.Join(parent.testActivity(parent.shell.Transcript).RenderFeed(100, 28).Lines, "\n"), "LOCAL_CASE") {
		t.Fatal("child command leaked into parent's Thread pane")
	}

	reopened, _ := newAppServerTestUI()
	reopened.ctx, reopened.Startup.Thread, reopened.nativeAgent = t.Context(), "child", child.nativeAgent
	appServerTestRequest(t, reopened, "1", "thread/resume")
	appServerTestMessage(t, reopened, `{"id":1,"result":{"thread":{"id":"child","turns":[{"id":"turn","status":"completed","items":[{"id":"before","type":"reasoning","summary":["**Local reasoning before**"]},{"id":"command","type":"commandExecution","command":"fixture-command LOCAL_CASE","status":"failed","exitCode":7,"aggregatedOutput":"LOCAL_CASE\n"},{"id":"after","type":"reasoning","summary":["**Local reasoning after**"]}]}]}}}`)
	assertTranscript(reopened)
}

func TestChildFamilyRosterPreservesMainSummary(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("child", t.TempDir())
	u.testMainFrame(100, 24, 0)
	rootAgent := activity.Agent{Name: "/root", Thread: "main", Summary: []activity.Entry{{Kind: "text", Text: "PARENT_SUMMARY", Agent: "Main", Values: []activity.Value{{Kind: "text", Body: "PARENT_SUMMARY"}}}}}
	family := &sessionapi.Family{Root: "main", Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}}, MainSummary: rootAgent.Summary}}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.event.Frame = sessionapi.NativeFrame{Family: family, View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: &activity.Publication{MainSummary: []activity.Entry{{Kind: "text", Text: "CHILD_SUMMARY", Agent: "Main", Values: []activity.Value{{Kind: "text", Body: "CHILD_SUMMARY"}}}}}}
	c.event.Frame.View.Info.Caller = "/root/worker"
	c.refreshView(time.Now())
	summary, _ := terminal.Agents.Current(rootAgent, time.Now(), 100)
	if !strings.Contains(ansi.Strip(summary), "PARENT_SUMMARY") {
		t.Fatalf("family main row lost its owner: %s", ansi.Strip(summary))
	}
	rootAgent.Summary = nil
	c.event.Frame.Family = &sessionapi.Family{Root: "main", Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}}}, Status: sessionpresentation.Label{Text: "Parent working"}}
	c.refreshView(time.Now())
	summary, _ = terminal.Agents.Current(rootAgent, time.Now(), 100)
	if !strings.Contains(ansi.Strip(summary), "Parent working") {
		t.Fatalf("family Main status borrowed child state: %s", summary)
	}

}

func TestChildFamilyRosterPreservesEditCounts(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("child", t.TempDir())
	u.testMainFrame(120, 24, 0)
	rootAgent := activity.Agent{Name: "/root", Thread: "main"}
	childAgent := activity.Agent{Name: "/root/worker", Thread: "child"}
	roster := &activity.Publication{Roster: []activity.AgentRow{{Agent: rootAgent}, {Agent: childAgent, Depth: 1}}, LineCounts: map[string]diffmodel.Counts{"/root": {Added: 100}, "/root/worker": {Added: 3}}, NetCounts: new(diffmodel.Counts{Added: 103})}
	family := &sessionapi.Family{Root: "main", Agents: roster}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.event.Frame = sessionapi.NativeFrame{Family: family, View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: &activity.Publication{LineCounts: map[string]diffmodel.Counts{"/root": {Added: 3}}, NetCounts: new(diffmodel.Counts{Added: 3})}}
	c.event.Frame.View.Info.Caller = "/root/worker"
	c.refreshView(time.Now())
	rows := ansi.Strip(strings.Join(terminal.Agents.NativeRoster(120, 10, time.Now(), false), "\n"))
	if !strings.Contains(rows, "+100") || !strings.Contains(rows, "+103") {
		t.Fatalf("selected child changed family roster edit ownership:\n%s", rows)
	}
}

func TestPreparedChildStartsWithItsOwnReviewWorkspace(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.nativeAgent = &binding.ThreadInfo{ID: "child", ParentThreadID: "main", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker"}}}`)}
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"child","cwd":"/var/tmp/child-worktree","turns":[]}}}`)
	view := u.reviewSource.ReviewView(u.attached.session.Scope())
	if view.Workspace != "/var/tmp/child-worktree" || !u.review.Scope().Workspaces[view.Workspace]["child"] {
		t.Fatalf("fresh child borrowed another review workspace: %+v", view)
	}
}
