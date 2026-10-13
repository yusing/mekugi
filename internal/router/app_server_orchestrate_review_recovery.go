package router

import (
	"context"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func workflowUnsettled(batch orchestrate.Batch) bool {
	if batch.Workflow == nil {
		return false
	}
	for _, d := range batch.Workflow.Dispatches {
		if d.State == orchestrate.DispatchIntent || d.State == orchestrate.DispatchUncertain || d.State == orchestrate.DispatchAcknowledged && d.HostStatus == "" && d.ResultItemID == "" {
			return true
		}
	}
	return false
}

// Restore retained workflow identities, then reconcile host history. This never
// replays a turn/start or treats an old running label as a revived process.
func (u *appServerUI) recoverOrchestratedWorkflows(batches []orchestrate.Batch) {
	for _, batch := range batches {
		if batch.Workflow == nil || batch.Workflow.Assignment.Kind != orchestrate.WorkflowImplementation || batch.State == "removed" || batch.State == "removing" {
			continue
		}
		child := &orchestrateChild{batch: batch, command: &orchestrateCommand{ctx: u.ctx, workspace: u.session.cwd, main: u.thread, reply: make(chan orchestrateResult, 1)}, workflowEnding: true, workflowDeferred: true}
		if batch.Launch == nil || batch.Launch.ThreadID == "" {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("implementation launch has no confirmed thread; reconcile host state"))
			continue
		}
		if u.orchestrateThreads == nil {
			u.orchestrateThreads = map[string]*orchestrateChild{}
		}
		u.orchestrateThreads[batch.Launch.ThreadID] = child
		if workflowTerminal(batch.Workflow) && !workflowUnsettled(batch) {
			u.workflowOutcome(child)
			continue
		}
		u.reconcileWorkflow(child)
	}
}

// reconcileWorkflow reads host history for every unsettled dispatch of the
// current generation, so concurrent reviews recover independently. Missing
// checks have no host effect to reconcile; driving reruns them.
func (u *appServerUI) reconcileWorkflow(child *orchestrateChild) {
	w := child.batch.Workflow
	var pending []orchestrate.WorkflowDispatch
	for _, d := range w.Dispatches {
		if d.Generation != w.Generation || d.State == orchestrate.DispatchFailed || d.ResultItemID != "" || child.workflowWork[d.Kind] != nil {
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
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
	}
	if m.Error != nil {
		fail(errors.New(m.Error.Message))
		return
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if json.Unmarshal(m.Result, &result) != nil || result.Thread.ID != work.dispatch.ThreadID || result.Thread.Cwd != child.batch.Cwd {
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
	for _, turn := range result.Thread.Turns {
		if turn.ID != turnID {
			continue
		}
		work.dispatch.TurnID = turnID
		if turn.Status != "completed" && turn.Status != "failed" && turn.Status != "interrupted" {
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
			u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
				return s.ObserveLaunch(ctx, c.workspace, c.main, child.batch.TaskName, turn.Status, "")
			}, func(err error) {
				if err != nil {
					fail(err)
				} else {
					u.workflowImplementationCompleted(child, *work.completed, work.message)
				}
			})
			return
		}
		u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
			return s.RecordWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, d.ID, d.ThreadID, d.TurnID)
		}, func(err error) {
			if err != nil {
				fail(err)
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
