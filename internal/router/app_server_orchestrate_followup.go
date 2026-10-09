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
	c.caller = c.main
	if c.main != u.thread || c.workspace != u.session.cwd {
		member := u.orchestrateThreads[c.main]
		if member == nil || member.batch.Cwd != c.workspace || member.command.main != u.thread || member.command.workspace != u.session.cwd {
			c.reply <- orchestrateResult{err: errors.New("caller is not a confirmed member of the active orchestration")}
			return
		}
		c.main, c.workspace = member.command.main, member.command.workspace
	}
	for thread, child := range u.orchestrateThreads {
		if child.command.main == c.main && child.command.workspace == c.workspace && (c.target != "main" && c.target == child.batch.TaskName || c.target == "/root/"+child.batch.TaskName) && thread != c.caller && child.batch.State == "launched" {
			child.followups = append(child.followups, c)
			if len(child.followups) == 1 {
				u.dispatchOrchestratedFollowup(child)
			}
			return
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
	u.orchestrateWork(func() func() {
		d, dispatch, err := store.BeginDelivery(c.ctx, c.workspace, c.main, orchestrate.Delivery{ID: c.callID, From: c.caller, Target: child.batch.Launch.ThreadID, Message: c.input.Message})
		return func() {
			if err != nil || !dispatch {
				u.finishOrchestratedFollowup(child, c, d, err)
				return
			}
			if c.ctx.Err() != nil || u.orchestrateClosing {
				u.retainOrchestratedDelivery(child, c, d, "canceled", "", context.Canceled)
				return
			}
			method := "turn/start"
			params := map[string]any{"threadId": d.Target, "input": appserver.Input(d.Message)}
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
	u.orchestrateWork(func() func() {
		message := ""
		if outcome != nil {
			message = outcome.Error()
		}
		result, err := store.RecordDelivery(ctx, c.workspace, c.main, d.ID, state, turn, message)
		return func() {
			if err != nil {
				u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
				u.setNotice("Orchestration delivery was not retained: "+err.Error(), true)
			}
			u.finishOrchestratedFollowup(child, c, result, errors.Join(outcome, err))
		}
	})
}

func (u *appServerUI) orchestratedFollowupResponse(r orchestrateRPC, m appserver.Message) {
	if m.Error != nil {
		u.retainOrchestratedDelivery(r.child, r.followup, r.delivery, "rejected", "", fmt.Errorf("%s: %s", r.method, m.Error.Message))
		return
	}
	var result struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := json.Unmarshal(m.Result, &result)
	turn := result.Turn.ID
	if r.method == "turn/steer" {
		turn = result.TurnID
	}
	if err != nil || turn == "" {
		u.retainOrchestratedDelivery(r.child, r.followup, r.delivery, "uncertain", "", errors.New("host returned no valid delivery turn identity"))
		return
	}
	if r.method == "turn/start" && r.child.completedTurn != turn {
		r.child.turn = turn
	}
	u.retainOrchestratedDelivery(r.child, r.followup, r.delivery, "delivered", turn, nil)
}
