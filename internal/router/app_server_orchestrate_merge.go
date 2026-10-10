package router

import "errors"

func (u *appServerUI) planOrchestratedMerge(c *orchestrateCommand) {
	for _, child := range u.orchestrateThreads {
		if child.command.main != c.main || child.command.workspace != c.workspace || c.target != child.batch.TaskName && c.target != "/root/"+child.batch.TaskName {
			continue
		}
		if u.navigation == nil {
			break
		}
		v := u.navigation.views[child.batch.Launch.ThreadID]
		if child.removing || child.turn != "" || len(child.followups) != 0 || v == nil || v.busy() || len(v.activeChildren) != 0 || v.reset.active() || v.questionCount() != 0 || len(v.approvals.pending) != 0 || len(v.btwRequests) != 0 || v.btw != nil && (v.btw.busy || v.btw.starting || v.btw.interrupting) {
			c.reply <- orchestrateResult{err: errors.New("shadow merge requires an idle subscribed child with no pending work")}
			return
		}
		for _, request := range u.orchestrateRequests {
			if request.child == child {
				c.reply <- orchestrateResult{err: errors.New("shadow merge requires settled child host requests")}
				return
			}
		}
		store, name, thread := u.proxy.orchestration.store, child.batch.TaskName, child.batch.Launch.ThreadID
		u.orchestrateWork(func() func() {
			batch, err := store.PlanShadowMerge(c.ctx, c.workspace, c.main, name, thread)
			return func() {
				if err == nil {
					child.batch.ShadowMerge = batch.ShadowMerge
				}
				c.reply <- orchestrateResult{batch: batch, err: err}
			}
		})
		return
	}
	c.reply <- orchestrateResult{err: errors.New("shadow merge target is not a subscribed batch in this run")}
}
