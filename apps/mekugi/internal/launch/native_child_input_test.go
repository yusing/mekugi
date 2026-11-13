package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestPreparedChildStartupPreservesObservationAudience(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.audience = sessioncontrol.NewAudience(nil)
	u.Startup.Thread = "child"
	u.nativeAgent = &appserver.AgentObservation{ThreadID: "child", RootThreadID: "main", ParentThreadID: new("main"), Path: new("/root/worker")}
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
	u.nativeAgent = &appserver.AgentObservation{ThreadID: "child", RootThreadID: "main", ParentThreadID: new("main"), Path: new("/root/worker")}
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"child","cwd":"/var/tmp/child-worktree","turns":[]}}}`)
	view := u.reviewSource.ReviewView(u.attached.session.Scope())
	if view.Workspace != "/var/tmp/child-worktree" || !u.review.Scope().Workspaces[view.Workspace]["child"] {
		t.Fatalf("fresh child borrowed another review workspace: %+v", view)
	}
}

// Publish the same ordinary source consumed by Hub.Family, independently of any
// native attachment or selected renderer.
func publishNativeFamilySource(api *sessionapi.Server, frame sessionapi.NativeFrame, workspace, root, caller, role string, members []sessionpresentation.Participant) error {
	return api.Publish(sessionapi.Frame{
		Session:    sessionapi.Session{Thread: frame.View.Main.ID, Workspace: workspace, Caller: caller, Role: role},
		FamilyRoot: root,
		Status:     frame.View.Status.State, Members: members,
		Main: frame.Main, Agents: frame.Agents, Review: frame.Review,
	})
}
