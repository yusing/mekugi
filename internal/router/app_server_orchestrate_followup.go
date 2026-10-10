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
		if c.deferred {
			store := u.proxy.orchestration.store
			u.orchestrateWork(func() func() {
				d, _, err := store.BeginDelivery(c.ctx, c.workspace, c.main, orchestrate.Delivery{ID: c.callID, From: c.caller, Target: c.main, Message: c.input.Message, Deferred: true})
				return func() { c.reply <- orchestrateResult{delivery: &d, err: err} }
			})
			return
		}
		u.orchestrateMainFollowups = append(u.orchestrateMainFollowups, c)
		if err := u.flushInput(); err != nil {
			u.setNotice(err.Error(), true)
		}
		return
	}
	for thread, child := range u.orchestrateThreads {
		if child.command.main == c.main && child.command.workspace == c.workspace && (c.target != "main" && c.target == child.batch.TaskName || c.target == "/root/"+child.batch.TaskName) && thread != c.caller && child.batch.State == "launched" {
			if u.navigation.resumes[thread] != nil {
				u.resumeOrchestratedBatch(child.batch.TaskName, func(err error) {
					if err != nil {
						c.reply <- orchestrateResult{err: err}
					} else {
						u.queueOrchestratedFollowup(c)
					}
				})
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
			if b.State == "launched" && b.Launch != nil && b.Launch.ThreadID != c.caller && n.views[b.Launch.ThreadID] == nil && (c.target != "main" && c.target == b.TaskName || c.target == "/root/"+b.TaskName) {
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
	u.orchestrateWork(func() func() {
		input := orchestrate.Delivery{ID: c.callID, From: c.caller, Target: child.batch.Launch.ThreadID, Message: c.input.Message, Deferred: c.deferred}
		var d orchestrate.Delivery
		var dispatch bool
		var err error
		text := input.Message
		if turn == "" && !c.deferred {
			d, text, dispatch, err = store.BeginTurnDelivery(c.ctx, c.workspace, c.main, input)
		} else {
			d, dispatch, err = store.BeginDelivery(c.ctx, c.workspace, c.main, input)
		}
		return func() {
			if err != nil || !dispatch {
				u.finishOrchestratedFollowup(child, c, d, err)
				return
			}
			if c.ctx.Err() != nil || u.orchestrateClosing {
				u.retainOrchestratedDelivery(child, c, d, "canceled", "", context.Canceled)
				return
			}
			if child.turn != turn {
				u.retainOrchestratedDelivery(child, c, d, "rejected", "", errors.New("target turn changed before delivery"))
				return
			}
			method := "turn/start"
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
	state, turn, err := orchestratedDeliveryResponse(r.method, m)
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
