package router

import (
	"errors"
	"os"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func orchestrateBatchState(batch orchestrate.Batch) string {
	if batch.Launch != nil && batch.Launch.HostStatus != "" {
		return batch.Launch.HostStatus
	}
	return batch.State
}

func orchestrateRestoredBatchState(batch orchestrate.Batch) string {
	if state := orchestrateBatchState(batch); state != "running" {
		return state
	}
	return "interrupted"
}

// Retained facts restore discovery, not host subscriptions or process handles.
func (u *appServerUI) recoverOrchestratedRun() {
	if u.navigation != nil || u.proxy == nil || u.proxy.orchestration == nil || u.proxy.orchestration.store == nil {
		return
	}
	main, workspace, store := u.thread, u.session.cwd, u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		batches, err := store.Snapshot(workspace, main)
		return func() {
			if u.orchestrateClosing || u.thread != main || u.session.cwd != workspace {
				return
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				u.setNotice("Orchestration restore: "+err.Error(), true)
				return
			}
			if len(batches) == 0 {
				return
			}
			u.ensureOrchestrationNavigation().retained = batches
			u.orchestrationRoster()
		}
	})
}
