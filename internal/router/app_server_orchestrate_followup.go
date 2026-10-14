package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func (u *appServerUI) queueOrchestratedFollowup(c *orchestrateCommand) {
	if c.caller == "" {
		c.caller = c.main
	}
	if c.main != u.thread || c.workspace != u.session.cwd {
		member := u.orchestrateThreads[c.main]
		if member == nil || member.batch.Cwd != c.workspace || member.command.main != u.thread || member.command.workspace != u.session.cwd {
			c.reply <- orchestrateResult{err: errors.New("caller is not a confirmed member of the active orchestration")}
			return
		}
		c.main, c.workspace = member.command.main, member.command.workspace
	}
	if c.target == "main" && c.caller != u.thread {
		u.reportOrchestratedMainMessage(c)
		return
	}
	for thread, child := range u.orchestrateThreads {
		if child.command.main == c.main && child.command.workspace == c.workspace && (c.target != "main" && c.target == child.batch.TaskName || c.target == "/root/"+child.batch.TaskName) && thread != c.caller && child.batch.Launch != nil && child.batch.Launch.ThreadID != "" && child.batch.State != "removing" && child.batch.State != "removed" && child.batch.State != "integrating" {
			if u.navigation == nil || u.navigation.views[thread] == nil || u.navigation.resumes[thread] != nil {
				u.resumeOrchestratedBatch(child.batch.TaskName, func(err error) {
					if err != nil {
						c.reply <- orchestrateResult{err: err}
					} else {
						u.queueOrchestratedFollowup(c)
					}
				})
				return
			}
			if workflowInputReserved(child) {
				c.reply <- orchestrateResult{err: errors.New("runtime workflow owns this batch; interrupt it before manual follow-up")}
				return
			}
			child.followups = append(child.followups, c)
			if len(child.followups) == 1 {
				u.dispatchOrchestratedFollowup(child)
			}
			return
		}
	}
	if n := u.navigation; n != nil {
		for _, b := range n.retained {
			if b.State != "removing" && b.State != "removed" && b.Launch != nil && b.Launch.ThreadID != "" && b.Launch.ThreadID != c.caller && n.views[b.Launch.ThreadID] == nil && (c.target != "main" && c.target == b.TaskName || c.target == "/root/"+b.TaskName) {
				u.resumeOrchestratedBatch(b.TaskName, func(err error) {
					if err != nil {
						c.reply <- orchestrateResult{err: err}
					} else {
						u.queueOrchestratedFollowup(c)
					}
				})
				return
			}
		}
	}
	c.reply <- orchestrateResult{err: errors.New("target is not a separate launched batch in this run")}
}

func (u *appServerUI) finishOrchestratedFollowup(child *orchestrateChild, c *orchestrateCommand, delivery orchestrate.Delivery, err error) {
	c.reply <- orchestrateResult{delivery: &delivery, err: err}
	child.followups[0] = nil
	child.followups = child.followups[1:]
	if len(child.followups) != 0 {
		u.dispatchOrchestratedFollowup(child)
	}
}

func (u *appServerUI) dispatchOrchestratedFollowup(child *orchestrateChild) {
	c := child.followups[0]
	store := u.proxy.orchestration.store
	turn := child.turn
	first := child.batch.Launch.TurnUnsent
	u.orchestrateBatchWork(c.workspace, c.main, child.batch.TaskName, func() func() {
		input := orchestrate.Delivery{ID: c.callID, From: c.caller, Target: child.batch.Launch.ThreadID, Message: c.input.Message, TurnID: turn, Deferred: c.tool == "send_message", ReviewWaiver: c.reviewWaiver, ReviewScope: c.reviewScope}
		d, text, dispatch, err := store.BeginTurnDelivery(c.ctx, c.workspace, c.main, input)
		var amended *orchestrate.Batch
		if err == nil && c.reviewScope != "" {
			var batches []orchestrate.Batch
			batches, err = store.Snapshot(c.workspace, c.main)
			for _, batch := range batches {
				if batch.TaskName == child.batch.TaskName {
					amended = &batch
					break
				}
			}
		}
		return func() {
			if amended != nil {
				child.batch.Workflow, child.batch.Integration = amended.Workflow, amended.Integration
			}
			if err != nil || !dispatch {
				u.finishOrchestratedFollowup(child, c, d, err)
				return
			}
			if c.ctx.Err() != nil || u.orchestrateClosing {
				u.retainOrchestratedDelivery(child, c, d, "canceled", "", context.Canceled)
				return
			}
			if child.turn != turn || first && !child.batch.Launch.TurnUnsent {
				if d.Deferred && !first && turn != "" && child.completedTurn == turn {
					// No host request was sent. Keep the original arrival position.
					u.retainOrchestratedDelivery(child, c, d, "queued", "", nil)
					return
				}
				u.retainOrchestratedDelivery(child, c, d, "rejected", "", errors.New("target lifecycle changed before delivery"))
				return
			}
			method := "turn/start"
			if first {
				child.batch.Launch.TurnUnsent = false
				text = orchestrateFirstInput(child.batch) + "\n" + text
			}
			params := map[string]any{"threadId": d.Target, "input": appserver.Input(text)}
			if child.turn != "" {
				method = "turn/steer"
				params["expectedTurnId"] = child.turn
			}
			request := orchestrateRPC{method: method, child: child, followup: c, delivery: d}
			if err := u.orchestrateRPCRequest(request, params); err != nil {
				u.retainOrchestratedDelivery(child, c, d, "uncertain", "", err)
				return
			}
		}
	})
}

func (u *appServerUI) retainOrchestratedDelivery(child *orchestrateChild, c *orchestrateCommand, d orchestrate.Delivery, state, turn string, outcome error) {
	ctx, store := u.orchestrateStorageContext, u.proxy.orchestration.store
	u.orchestrateBatchWork(c.workspace, c.main, child.batch.TaskName, func() func() {
		message := ""
		if outcome != nil {
			message = outcome.Error()
		}
		result, err := store.RecordDelivery(ctx, c.workspace, c.main, d.ID, state, turn, message)
		return func() {
			if err == nil && child.batch.Launch.TurnID == "" {
				child.batch.Launch.TurnUnsent = (result.State == "canceled" || result.State == "rejected") && child.batch.Launch.HostStatus == ""
				if result.State == "delivered" {
					child.batch.Launch.TurnID = result.TurnID
					if child.workflowCompletion != nil {
						u.workflowImplementationCompleted(child, *child.workflowCompletion, child.workflowReport)
					}
				}
			}
			if err != nil {
				u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
				u.setNotice("Orchestration delivery was not retained: "+err.Error(), true)
			}
			u.finishOrchestratedFollowup(child, c, result, errors.Join(outcome, err))
		}
	})
}

func (u *appServerUI) orchestratedFollowupResponse(r orchestrateRPC, m appserver.Message) {
	state, turn, err := orchestratedDeliveryResponse(r.method, m)
	// Codex maps NotSubmitted to this exact invalid-request response, before
	// consuming input (app-server turn_processor.rs at ddb33e34b). Neither a
	// transport failure nor similar error text establishes that no effect occurred.
	if r.delivery.Deferred && r.method == "turn/steer" && m.Error != nil && m.Error.Code == -32600 && m.Error.Message == "no active turn to steer" {
		state, turn, err = "queued", "", nil
	}
	if err == nil && r.method == "turn/start" && r.child.completedTurn != turn {
		r.child.turn = turn
	}
	u.retainOrchestratedDelivery(r.child, r.followup, r.delivery, state, turn, err)
}

func orchestratedDeliveryResponse(method string, m appserver.Message) (state, turn string, err error) {
	if m.Error != nil {
		return "rejected", "", fmt.Errorf("%s: %s", method, m.Error.Message)
	}
	var result struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err = json.Unmarshal(m.Result, &result)
	turn = result.Turn.ID
	if method == "turn/steer" {
		turn = result.TurnID
	}
	if err != nil || turn == "" {
		return "uncertain", "", errors.New("host returned no valid delivery turn identity")
	}
	return "delivered", turn, nil
}
