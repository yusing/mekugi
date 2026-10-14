package router

import (
	"context"
	"errors"
)

func (u *appServerUI) archiveOrchestratedChild(c *orchestrateCommand, child *orchestrateChild, reply func(orchestrateResult)) {
	name := child.batch.TaskName
	u.orchestrateBatchWork(c.workspace, c.main, name, func() func() {
		batch, err := u.proxy.orchestration.store.BeginArchive(c.ctx, c.workspace, c.main, name, func(publish func() error) error {
			_, err := u.proxy.orchestrateCleanupProof(c.ctx, c.workspace, c.main, name, publish)
			return err
		})
		return func() {
			if batch.TaskName != "" {
				child.batch = batch
			}
			if err != nil || batch.Archive.State == "archived" {
				child.removing = false
				reply(orchestrateResult{batch: child.batch, err: err})
				return
			}
			if err := u.orchestrateCleanupIdle(child, u.navigation.views[batch.Launch.ThreadID]); err != nil {
				u.recordOrchestratedArchive(child, reply, false, err)
				return
			}
			if err := c.ctx.Err(); err != nil {
				u.recordOrchestratedArchive(child, reply, false, err)
				return
			}
			if u.orchestrateClosing {
				u.recordOrchestratedArchive(child, reply, false, context.Canceled)
				return
			}
			if err := u.orchestrateRPCRequest(orchestrateRPC{method: "thread/archive", child: child, archiveReply: reply}, map[string]any{"threadId": batch.Launch.ThreadID}); err != nil {
				u.recordOrchestratedArchive(child, reply, true, err)
			}
		}
	})
}

func (u *appServerUI) recordOrchestratedArchive(child *orchestrateChild, reply func(orchestrateResult), dispatched bool, hostErr error) {
	c, name := child.command, child.batch.TaskName
	u.orchestrateBatchWork(c.workspace, c.main, name, func() func() {
		batch, err := u.proxy.orchestration.store.RecordArchive(u.orchestrateStorageContext, c.workspace, c.main, name, dispatched, hostErr)
		return func() {
			child.removing = false
			if batch.TaskName != "" {
				child.batch = batch
			}
			reply(orchestrateResult{batch: child.batch, err: errors.Join(hostErr, err)})
		}
	})
}
