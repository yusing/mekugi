package router

import "github.com/yusing/mekugi/internal/orchestrate"

// Main receives child reports through its tool observation queue. Only the
// human composer can create user input or steer the coordinator's turn.
func (u *appServerUI) reportOrchestratedMainMessage(c *orchestrateCommand) {
	child := u.orchestrateThreads[c.caller]
	store := u.proxy.orchestration.store
	u.orchestrateBatchWork(c.workspace, c.main, child.batch.TaskName, func() func() {
		d, publish, err := store.BeginDelivery(c.ctx, c.workspace, c.main, orchestrate.Delivery{
			ID: c.callID, From: c.caller, Target: c.main, Message: c.input.Message, Deferred: c.tool == "send_message",
		})
		return func() {
			if err == nil && publish {
				from := "/root/" + child.batch.TaskName
				u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: from, Kind: "reply", Text: d.Message, Observed: u.now(),
					message: &activityMessage{from: from, to: "/root", text: d.Message},
					native:  &liveActivityNativeItem{thread: c.caller, turn: "collaboration", item: c.callID, phase: "item/completed", orchestrated: true}}}, u.session.agents)
				u.advanceOrchestratedWaits(c.workspace, c.main, nil)
			}
			c.reply <- orchestrateResult{delivery: &d, err: err}
		}
	})
}
