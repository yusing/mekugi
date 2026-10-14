package router

import (
	"errors"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func (u *appServerUI) planOrchestratedMerge(c *orchestrateCommand) {
	resume := func(batch orchestrate.Batch) {
		if batch.VCS != "shadow" && batch.VCS != "svn" {
			c.reply <- orchestrateResult{err: errors.New("integrate requires a shadow batch; use native Git for Git batches")}
			return
		}
		if w := batch.Workflow; w != nil && (w.Assignment.Kind != orchestrate.WorkflowImplementation || w.Phase != orchestrate.PhaseReady || workflowUnsettled(batch)) {
			c.reply <- orchestrateResult{err: errors.New("integration requires a ready workflow with settled host effects")}
			return
		}
		u.resumeOrchestratedBatch(batch.TaskName, func(err error) {
			if err != nil {
				c.reply <- orchestrateResult{err: err}
			} else {
				u.planOrchestratedMerge(c)
			}
		})
	}
	for _, child := range u.orchestrateThreads {
		if child.command.main != c.main || child.command.workspace != c.workspace || c.target != child.batch.TaskName && c.target != "/root/"+child.batch.TaskName {
			continue
		}
		if u.navigation == nil {
			break
		}
		if u.navigation.resumes[child.batch.Launch.ThreadID] != nil || u.navigation.views[child.batch.Launch.ThreadID] == nil {
			resume(child.batch)
			return
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
		previous := child.batch
		child.removing = true
		child.batch.State = "integrating" // Reserve input before leaving the UI loop.
		u.orchestrateBatchWork(c.workspace, c.main, name, func() func() {
			batch, err := store.IntegrateShadow(c.ctx, c.workspace, c.main, name, thread)
			return func() {
				child.removing = false
				child.batch = previous
				if batch.TaskName != "" {
					child.batch = batch
				}
				c.reply <- orchestrateResult{batch: batch, err: err}
			}
		})
		return
	}
	if n := u.navigation; n != nil {
		for _, batch := range n.retained {
			if (c.target == batch.TaskName || c.target == "/root/"+batch.TaskName) && batch.Launch != nil && batch.Launch.ThreadID != "" && batch.State != "removed" && batch.State != "removing" && n.views[batch.Launch.ThreadID] == nil {
				resume(batch)
				return
			}
		}
	}
	c.reply <- orchestrateResult{err: errors.New("shadow merge target is not a subscribed batch in this run")}
}
