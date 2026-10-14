package router

import (
	"errors"
	"os"
	"strings"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func orchestrateStateLabel(state string) string {
	state = strings.ReplaceAll(state, "_", " ")
	if state != "" {
		state = strings.ToUpper(state[:1]) + state[1:]
	}
	return state
}

func orchestrateBatchState(batch orchestrate.Batch) string {
	if batch.State == "integrating" || batch.State == "removing" || batch.State == "removed" {
		return batch.State
	}
	if batch.Workflow != nil && batch.Workflow.Assignment.Kind == orchestrate.WorkflowImplementation {
		return string(batch.Workflow.Phase)
	}
	if batch.Launch != nil {
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
	generation := u.orchestrateGeneration
	u.orchestrateWork(func() func() {
		batches, err := store.Snapshot(workspace, main)
		var waits []orchestrate.Wait
		var waitErr error
		if err == nil {
			err = store.RecoverWaits(u.orchestrateStorageContext, workspace, main)
		}
		if err == nil {
			waits, waitErr = store.Waits(workspace, main, "registered", "ready", "dispatching", "delivered", "uncertain", "rejected", "canceled")
		}
		return func() {
			if u.orchestrateClosing || u.orchestrateGeneration != generation || u.thread != main || u.session.cwd != workspace {
				return
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				u.setNotice("Orchestration restore: "+err.Error(), true)
				return
			}
			if err == nil {
				if waitErr != nil {
					u.setNotice("Wait results could not be restored: "+waitErr.Error(), true)
				}
				for _, wait := range waits {
					u.observeOrchestrationWait(workspace, main, wait)
					if result := u.session.orchestrationWaitResults[wait.ID]; result != nil {
						result.restored = true
					}
				}
				u.advanceOrchestratedWaits(workspace, main, nil)
			}
			if len(batches) == 0 {
				return
			}
			u.ensureOrchestrationNavigation().retained = batches
			u.orchestrationRoster()
			for _, batch := range batches {
				if batch.Workflow == nil || batch.Workflow.Assignment.Kind != orchestrate.WorkflowDiagnosis || batch.Workflow.Outcome == nil || batch.Launch == nil || batch.Launch.ThreadID == "" {
					continue
				}
				child := &orchestrateChild{batch: batch, command: &orchestrateCommand{ctx: u.ctx, workspace: workspace, main: main}, completedTurn: batch.Workflow.Outcome.TurnID}
				if u.orchestrateThreads == nil {
					u.orchestrateThreads = map[string]*orchestrateChild{}
				}
				u.orchestrateThreads[batch.Launch.ThreadID] = child
				u.deliverDiagnosisOutcome(child)
			}
			u.recoverOrchestratedWorkflows(batches)
		}
	})
}
