package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func workflowUnsettled(batch orchestrate.Batch) bool {
	if batch.Workflow == nil {
		return false
	}
	for _, d := range batch.Workflow.Dispatches {
		if workflowDispatchUnsettled(d) {
			return true
		}
	}
	return false
}

func workflowDispatchUnsettled(d orchestrate.WorkflowDispatch) bool {
	return d.State == orchestrate.DispatchIntent || d.State == orchestrate.DispatchUncertain || d.State == orchestrate.DispatchAcknowledged && d.HostStatus == "" && d.ResultItemID == ""
}

// Restore retained workflow identities, then reconcile host history. This never
// replays a turn/start or treats an old running label as a revived process.
func (u *appServerUI) recoverOrchestratedWorkflows(batches []orchestrate.Batch) {
	for _, batch := range batches {
		if batch.Workflow == nil || batch.Workflow.Assignment.Kind != orchestrate.WorkflowImplementation {
			continue
		}
		child := &orchestrateChild{batch: batch, command: &orchestrateCommand{ctx: u.ctx, workspace: u.session.cwd, main: u.thread, reply: make(chan orchestrateResult, 1)}, workflowEnding: true, workflowDeferred: true}
		if batch.Launch == nil || batch.Launch.ThreadID == "" {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("implementation launch has no confirmed thread; reconcile host state"))
			continue
		}
		for _, d := range batch.Workflow.Dispatches {
			if d.Scratch != "" && (d.Archive != nil && d.Archive.State == "archived" || d.State == orchestrate.DispatchFailed && d.ThreadID == "") {
				u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
					return s.ReleaseWorkflowReviewStorage(ctx, child.command.workspace, child.command.main, batch.TaskName, d.ID)
				}, func(err error) {
					if err != nil {
						u.setNotice("Reviewer storage: "+err.Error(), true)
					}
				})
			}
			if d.ThreadID == "" || d.Kind != orchestrate.DispatchCorrectness && d.Kind != orchestrate.DispatchSimplification {
				continue
			}
			raw, _ := json.Marshal(map[string]any{"thread": appServerThreadInfo{ID: d.ThreadID, Cwd: batch.Cwd}})
			if err := u.addWorkflowReviewView(child, d, raw, true, func() {}); err != nil {
				u.setNotice("Review view: "+err.Error(), true)
			}
		}
		if batch.State == "removed" || batch.State == "removing" {
			continue
		}
		if u.orchestrateThreads == nil {
			u.orchestrateThreads = map[string]*orchestrateChild{}
		}
		u.orchestrateThreads[batch.Launch.ThreadID] = child
		u.reconcileWorkflow(child)
	}
}

func (u *appServerUI) reconcileCorrectionLifecycle(child *orchestrateChild) {
	w := child.batch.Workflow
	// A correction advances the candidate generation before its independent
	// journal necessarily reaches disk. Reconcile that lifecycle even after
	// reviews settle, without submitting the old result to workflow policy again.
	if child.turn == "" && child.lifecycleRecovery == nil && child.workflowWork[orchestrate.DispatchCorrection] == nil {
		for _, d := range slices.Backward(w.Dispatches) {
			if d.Kind != orchestrate.DispatchCorrection {
				continue
			}
			if (d.Generation != w.Generation || w.Phase != orchestrate.PhaseCorrection) && d.State == orchestrate.DispatchAcknowledged && d.ThreadID == child.batch.Launch.ThreadID && d.TurnID != "" && d.HostStatus != "" {
				work := &orchestrateReviewWork{child: child, dispatch: d, lifecycleOnly: true}
				child.lifecycleRecovery = work
				if err := u.orchestrateRPCRequest(orchestrateRPC{method: "thread/read", child: child, review: work}, map[string]any{"threadId": d.ThreadID, "includeTurns": true}); err != nil {
					u.endWorkflowWork(work)
					u.setNotice("Correction lifecycle: "+err.Error(), true)
				}
			}
			break
		}
	}
}

// reconcileWorkflow reads host history for every unsettled dispatch of the
// current generation, so concurrent reviews recover independently. Missing
// validation results cannot prove that a command was never dispatched.
func (u *appServerUI) reconcileWorkflow(child *orchestrateChild) {
	if child.batch.Launch.TurnUnsent {
		// Only explicit coordinator input may start this known-unsent turn.
		child.workflowEnding = false
		return
	}
	u.reconcileCorrectionLifecycle(child)
	w := child.batch.Workflow
	if workflowTerminal(w) && !workflowUnsettled(child.batch) {
		u.workflowOutcome(child)
		return
	}
	var pending []orchestrate.WorkflowDispatch
	for _, d := range w.Dispatches {
		if d.Kind == workflowValidation {
			if work := child.workflowWork[d.Kind]; work != nil && work.dispatch.ID == d.ID {
				continue
			}
			if workflowDispatchUnsettled(d) {
				u.workflowFailure(child, orchestrate.PhaseNeedsDecision, fmt.Errorf("validation check %s at %s has uncertain effects; reconcile host state without rerunning", d.CheckID, d.Revision))
				return
			}
			continue
		}
		if d.Generation != w.Generation || d.State == orchestrate.DispatchFailed || d.ResultItemID != "" || d.Archive != nil || child.workflowWork[d.Kind] != nil {
			continue
		}
		if d.Kind == orchestrate.DispatchCorrection && w.Phase != orchestrate.PhaseCorrection && d.HostStatus != "" {
			continue
		}
		pending = append(pending, d)
	}
	if len(pending) == 0 && w.Phase == orchestrate.PhaseImplementation && child.workflowWork[""] == nil {
		pending = append(pending, orchestrate.WorkflowDispatch{ThreadID: child.batch.Launch.ThreadID, TurnID: child.batch.Launch.TurnID})
	}
	if len(pending) == 0 {
		u.driveOrchestrateWorkflows()
		return
	}
	for _, d := range pending {
		if d.ThreadID == "" {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("dispatch has no confirmed thread; reconcile host state without relaunching"))
			return
		}
	}
	for _, d := range pending {
		work := &orchestrateReviewWork{child: child, dispatch: d}
		u.setWorkflowWork(child, work)
		if err := u.orchestrateRPCRequest(orchestrateRPC{method: "thread/read", child: child, review: work}, map[string]any{"threadId": d.ThreadID, "includeTurns": true}); err != nil {
			u.endWorkflowWork(work)
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
			return
		}
	}
}

func (u *appServerUI) workflowRecoveryResponse(work *orchestrateReviewWork, m appserver.Message) {
	child := work.child
	if child.batch.Launch != nil && u.orchestrateThreads[child.batch.Launch.ThreadID] != child {
		return
	}
	fail := func(err error) {
		u.endWorkflowWork(work)
		if work.lifecycleOnly {
			u.setNotice("Correction lifecycle: "+err.Error(), true)
			return
		}
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
	}
	if m.Error != nil {
		fail(errors.New(m.Error.Message))
		return
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if json.Unmarshal(m.Result, &result) != nil || result.Thread.ID != work.dispatch.ThreadID || result.Thread.Cwd != child.batch.Cwd || work.dispatch.Kind == orchestrate.DispatchCorrection && result.Thread.ParentThreadID != "" {
		fail(errors.New("recovered workflow thread identity mismatch"))
		return
	}
	turnID := work.dispatch.TurnID
	if turnID == "" {
		if (work.dispatch.Kind != orchestrate.DispatchCorrectness && work.dispatch.Kind != orchestrate.DispatchSimplification) || len(result.Thread.Turns) != 1 {
			fail(errors.New("dispatch turn identity is uncertain; inspect host history"))
			return
		}
		turnID = result.Thread.Turns[0].ID
	}
	for index, turn := range result.Thread.Turns {
		if turn.ID != turnID {
			continue
		}
		work.dispatch.TurnID = turnID
		if work.dispatch.Kind == orchestrate.DispatchCorrection {
			// History cannot settle a newer turn observed by either the host
			// snapshot or the live controller while this read was in flight.
			if child.turn != "" && child.turn != turnID || slices.ContainsFunc(result.Thread.Turns, func(t appServerHistoryTurn) bool {
				return t.ID != turnID && t.Status != "completed" && t.Status != "failed" && t.Status != "interrupted"
			}) {
				u.endWorkflowWork(work)
				return
			}
		}
		terminal := turn.Status == "completed" || turn.Status == "failed" || turn.Status == "interrupted"
		if work.lifecycleOnly && (!terminal || turn.Status != work.dispatch.HostStatus) {
			fail(errors.New("correction terminal result does not match host history"))
			return
		}
		if terminal && work.dispatch.Kind == orchestrate.DispatchCorrection {
			// Journal host observation uses the same synchronous owner as live
			// lifecycle events. A later start must not overtake this observation
			// while launch persistence waits in its ordered storage queue.
			// The launch precedes every correction. Host history also proves
			// earlier turns without treating an unknown active turn as old.
			previous := []string{child.batch.Launch.TurnID}
			for _, earlier := range result.Thread.Turns[:index] {
				previous = append(previous, earlier.ID)
			}
			matched, superseded, err := u.proxy.journals.reconcileRootHostTurn(u.ctx, u.proxy.replayStore, child.batch.Cwd, result.Thread.ID, turnID, turn.Status, previous)
			if err != nil {
				fail(err)
				return
			}
			if superseded || work.lifecycleOnly && !matched {
				u.endWorkflowWork(work)
				return
			}
		}
		if !terminal {
			if work.dispatch.Kind == "" {
				// A host read confirms active implementation; it does not revive it.
				child.turn = turnID
				u.endWorkflowWork(work)
				return
			}
			c, d := child.command, work.dispatch
			u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
				return s.RecordWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, d.ID, d.ThreadID, d.TurnID)
			}, func(err error) {
				if err != nil {
					fail(err)
					return
				}
				work.acknowledged = true
				u.finishWorkflowWork(work)
			})
			if child.workflowDeferred {
				u.setNotice("Workflow "+child.batch.TaskName+" awaits explicit resume", false)
				return
			}
			return
		}
		if u.navigation != nil {
			if v := u.navigation.views[work.dispatch.ThreadID]; v != nil && v.workflowReview != nil {
				v.restoreWorkflowReviewStatus(turn.Status)
			}
		}
		work.completed = &appServerTurn{ID: turn.ID, Status: turn.Status, Error: turn.Error}
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Delivery != "async" {
				work.message, work.item, work.messageTurn = item.Text, item.ID, turn.ID
			}
		}
		c, d := child.command, work.dispatch
		if d.Kind == "" {
			u.endWorkflowWork(work)
			child.turn = ""
		}
		u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
			if d.Kind == "" || d.Kind == orchestrate.DispatchCorrection {
				message := ""
				if turn.Error != nil {
					message = turn.Error.Message
				}
				if err := s.ObserveLaunch(ctx, c.workspace, c.main, child.batch.TaskName, turn.Status, message); err != nil {
					return err
				}
			}
			if work.lifecycleOnly || d.Kind == "" {
				return nil
			}
			return s.RecordWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, d.ID, d.ThreadID, d.TurnID)
		}, func(err error) {
			if err != nil {
				fail(err)
			} else if work.lifecycleOnly {
				u.endWorkflowWork(work)
			} else if d.Kind == "" {
				u.workflowImplementationCompleted(child, *work.completed, work.message)
			} else if d.Kind == orchestrate.DispatchCorrection && child.turn != "" && child.turn != turnID {
				u.endWorkflowWork(work)
			} else {
				work.acknowledged = true
				child.turn = ""
				u.finishWorkflowWork(work)
			}
		})
		return
	}
	fail(errors.New("recorded workflow turn is absent from host history"))
}
