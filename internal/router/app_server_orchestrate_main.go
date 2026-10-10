package router

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// Main input uses the composer gate and submission owner. The reservation keeps
// ordinary composer input from racing the off-loop delivery intent write.
type orchestrateMainInput struct {
	command    *orchestrateCommand
	delivery   orchestrate.Delivery
	dispatched bool
}

func (u *appServerUI) flushOrchestratedMainFollowup() error {
	if len(u.orchestrateMainFollowups) == 0 || u.orchestrateClosing {
		return nil
	}
	if ready, err := u.checkGuardHook(); !ready || err != nil {
		if err != nil {
			u.rejectOrchestratedMainFollowups(err)
		}
		return err
	}
	c := u.orchestrateMainFollowups[0]
	u.orchestrateMainFollowups = u.orchestrateMainFollowups[1:]
	pending := &orchestrateMainInput{command: c}
	u.orchestrateMainInput = pending
	turn, store := u.turn, u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		d, dispatch, err := store.BeginDelivery(c.ctx, c.workspace, c.main, orchestrate.Delivery{ID: c.callID, From: c.caller, Target: c.main, Message: c.input.Message})
		return func() {
			pending.delivery = d
			if err != nil || !dispatch {
				u.finishOrchestratedMainInput(pending, d, err)
				return
			}
			if c.ctx.Err() != nil || u.orchestrateClosing {
				u.retainOrchestratedMainInput(pending, "canceled", "", context.Canceled)
				return
			}
			if u.turn != turn || !u.acceptsInput() || u.restoring != nil || u.reset.active() || u.waitingQuestion() {
				u.retainOrchestratedMainInput(pending, "rejected", "", errors.New("coordinator input state changed before delivery"))
				return
			}
			pending.dispatched = true
			if err := u.send([]composerDraft{{text: c.input.Message, orchestrated: true}}, turn != ""); err != nil {
				pending.dispatched = false
				u.retainOrchestratedMainInput(pending, "uncertain", "", err)
			} else if !u.appServerInputOperation.pending() {
				pending.dispatched = false
				u.retainOrchestratedMainInput(pending, "rejected", "", errors.New("coordinator composer rejected input: "+u.notice))
			}
		}
	})
	return nil
}

func (u *appServerUI) cancelOrchestratedMainFollowups() {
	kept := u.orchestrateMainFollowups[:0]
	for _, c := range u.orchestrateMainFollowups {
		if err := c.ctx.Err(); err != nil {
			c.reply <- orchestrateResult{err: err}
		} else {
			kept = append(kept, c)
		}
	}
	clear(u.orchestrateMainFollowups[len(kept):])
	u.orchestrateMainFollowups = kept
}

func (u *appServerUI) rejectOrchestratedMainFollowups(err error) {
	for _, c := range u.orchestrateMainFollowups {
		c.reply <- orchestrateResult{err: err}
	}
	u.orchestrateMainFollowups = nil
}

func (u *appServerUI) finishOrchestratedMainInput(pending *orchestrateMainInput, d orchestrate.Delivery, err error) {
	pending.command.reply <- orchestrateResult{delivery: &d, err: err}
	u.orchestrateMainInput = nil
	if err := u.flushInput(); err != nil {
		u.setNotice(err.Error(), true)
	}
}

func (u *appServerUI) retainOrchestratedMainInput(pending *orchestrateMainInput, state, turn string, outcome error) {
	c, ctx, store := pending.command, u.orchestrateStorageContext, u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		message := ""
		if outcome != nil {
			message = outcome.Error()
		}
		d, err := store.RecordDelivery(ctx, c.workspace, c.main, pending.delivery.ID, state, turn, message)
		return func() {
			if err != nil {
				u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
				u.setNotice("Orchestration delivery was not retained: "+err.Error(), true)
			}
			u.finishOrchestratedMainInput(pending, d, errors.Join(outcome, err))
		}
	})
}

func (u *appServerUI) orchestratedMainResponse(method string, m appserver.Message) {
	pending := u.orchestrateMainInput
	if pending == nil || !pending.dispatched {
		return
	}
	pending.dispatched = false
	state, turn, err := orchestratedDeliveryResponse(method, m)
	u.retainOrchestratedMainInput(pending, state, turn, err)
}
