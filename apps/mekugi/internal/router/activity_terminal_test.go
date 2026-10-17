package router

import (
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func testActivityAgent(agent activityPaneAgent) activitypresentation.Agent {
	return activitypresentation.Agent{AgentFacts: agent.ActivityAgent()}
}

func (v *activityAdapter) testAgent(agent activityPaneAgent) activitypresentation.Agent {
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

func (v *activityAdapter) testCurrent(agent activityPaneAgent, now time.Time, widths ...int) (string, string) {
	return v.View.Current(v.testAgent(agent), now, widths...)
}

func (v *activityAdapter) testRoster() []activitypresentation.AgentRow {
	v.publishActivity()
	return v.View.Roster()
}

func (v *activityAdapter) testVisible(entry activityPaneEntry) bool {
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
		v.View.Hydrate(v.resolveOutputDemands(demands))
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
		v.View.Hydrate(v.resolveOutputDemands(demands))
	}
}

// Render one authored user item through the transcript's actual item consumer.
func (v *activityAdapter) testUserItem(entry activityPaneEntry, width int) activityui.Run {
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
