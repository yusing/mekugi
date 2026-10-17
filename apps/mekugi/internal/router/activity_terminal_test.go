package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestActivityRendererConsumesOnlyDeliveredFacts(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.applyActivity([]sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root", Kind: "error", Text: "delivered"}}, nil)
	render := func() string { return strings.Join(u.shell.Transcript.RenderFeed(80, 20).Lines, "\n") }
	if !strings.Contains(render(), "delivered") {
		t.Fatal("application did not deliver its admitted source")
	}
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 2, Agent: "Main", Kind: "text", Text: "next observation"}}})
	if strings.Contains(render(), "next observation") {
		t.Fatal("painting reached back into source state")
	}
	publishActivity(&u.attached, u.shell)
	if !strings.Contains(render(), "next observation") {
		t.Fatal("explicit delivery did not advance the renderer")
	}
}

func testActivityAgent(agent sessionpresentation.ActivityAgent) activitypresentation.Agent {
	return activitypresentation.Agent{AgentFacts: agent.ActivityAgent()}
}

func (v *activityAdapter) testAgent(agent sessionpresentation.ActivityAgent) activitypresentation.Agent {
	v.publishActivity()
	for _, row := range v.View.Roster() {
		if row.Agent.Name == agent.Name {
			// Synthetic lifecycle states must still exercise the requested clock
			// and animation state while consuming the real published summary.
			row.Agent.AgentFacts = agent.ActivityAgent()
			row.Agent.Status = v.Model.Status(agent)
			return row.Agent
		}
	}
	neutral := testActivityAgent(agent)
	neutral.Status = v.Model.Status(agent)
	neutral.Summary = v.Model.SummaryEntries(agent.Name, agent.Name == "/root")
	return neutral
}

func (v *activityAdapter) testCurrent(agent sessionpresentation.ActivityAgent, now time.Time, widths ...int) (string, string) {
	return v.View.Current(v.testAgent(agent), now, widths...)
}

func (v *activityAdapter) testRoster() []activitypresentation.AgentRow {
	v.publishActivity()
	return v.View.Roster()
}

func (v *activityAdapter) testVisible(entry sessionpresentation.ActivityEntry) bool {
	v.publishActivity()
	return v.View.Visible(activitypresentation.Entry{Facts: entry.ActivityFacts()})
}

func (v *activityAdapter) testRenderConversation(width int) activityui.Feed {
	v.publishActivity()
	for {
		feed := v.View.RenderConversation(width)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return feed
		}
		v.View.Hydrate(v.ResolveOutputDemands(demands))
	}
}

func (v *activityAdapter) testConversationItem(first, last, width int, thread activityui.ConversationThread) activityui.Run {
	v.publishActivity()
	for {
		run := v.View.ConversationItem(first, last, width, thread)
		demands := v.View.OutputDemands()
		if len(demands) == 0 {
			return run
		}
		v.View.Hydrate(v.ResolveOutputDemands(demands))
	}
}

// Render one authored user item through the transcript's actual item consumer.
func (v *activityAdapter) testUserItem(entry sessionpresentation.ActivityEntry, width int) activityui.Run {
	view := activityui.NewView()
	view.Painter = v.Painter
	view.Update(activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: entry.ActivityFacts()}}}, activityui.Display{})
	return view.ConversationItem(0, 0, width, activityui.ConversationThread{})
}

func (v *activityAdapter) testHandleMouse(action byte, row, column int) bool {
	v.publishActivity()
	handled := v.View.HandleMouse(action, row, column)
	v.applyDisplayIntent()
	return handled
}

func (v *activityAdapter) testPointAgent(action byte, row, column int) bool {
	v.publishActivity()
	handled := v.View.PointAgent(action, row, column)
	v.applyDisplayIntent()
	return handled
}

func (v *activityAdapter) testShowAgent(step int) {
	v.publishActivity()
	v.View.ShowAgent(step)
	v.applyDisplayIntent()
}

func (v *activityAdapter) testPace(now time.Time) bool {
	v.publishActivity()
	return v.View.Pace(now)
}

func (v *activityAdapter) testKeepSelection() {
	v.publishActivity()
	v.View.KeepSelection()
}

// Fixtures choose semantic admission separately from the terminal layout flags.
func (v *activityAdapter) testAudience() {
	v.Audience = 0
	if v.Conversation {
		v.Audience = sessionpresentation.MainAudience
	}
	if v.ChildrenOnly {
		v.Audience = sessionpresentation.ChildAudience
	}
}
