package router

import (
	"context"
	"crypto/rand"
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
	parts      []composerDraft // Ordinary user input, separate from child-owned text.
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
	u.beginOrchestratedMainInput(&orchestrateMainInput{command: c})
	return nil
}

func (u *appServerUI) reserveOrchestratedMainTurn(parts []composerDraft) {
	c := &orchestrateCommand{ctx: u.ctx, workspace: u.session.cwd, main: u.thread, callID: "composer/" + rand.Text()}
	u.beginOrchestratedMainInput(&orchestrateMainInput{command: c, parts: parts})
}

func (u *appServerUI) beginOrchestratedMainInput(pending *orchestrateMainInput) {
	c := pending.command
	u.orchestrateMainInput = pending
	turn, store := u.turn, u.proxy.orchestration.store
	u.orchestrateWork(func() func() {
		var d orchestrate.Delivery
		var dispatch bool
		var err error
		text := c.input.Message
		input := orchestrate.Delivery{ID: c.callID, From: c.caller, Target: c.main, Message: text}
		switch {
		case pending.parts != nil:
			d, text, dispatch, err = store.ReserveMainTurn(c.ctx, c.workspace, c.main, c.callID)
		case turn == "":
			d, text, dispatch, err = store.BeginTurnDelivery(c.ctx, c.workspace, c.main, input)
		default:
			d, dispatch, err = store.BeginDelivery(c.ctx, c.workspace, c.main, input)
		}
		return func() {
			pending.delivery = d
			if err != nil || !dispatch && pending.parts == nil {
				u.restoreDrafts(pending.parts...)
				u.finishOrchestratedMainInput(pending, d, err)
				return
			}
			if c.ctx.Err() != nil || u.orchestrateClosing {
				u.restoreDrafts(pending.parts...)
				u.retainOrchestratedMainInput(pending, "canceled", "", context.Canceled)
				return
			}
			if u.turn != turn || !u.acceptsInput() || u.restoring != nil || u.reset.active() || u.waitingQuestion() {
				u.restoreDrafts(pending.parts...)
				u.retainOrchestratedMainInput(pending, "rejected", "", errors.New("coordinator input state changed before delivery"))
				return
			}
			pending.dispatched = true
			parts := pending.parts
			if text != "" {
				parts = append([]composerDraft{{text: text, orchestrated: true}}, parts...)
			}
			if err := u.send(parts, turn != ""); err != nil {
				pending.dispatched = false
				u.retainOrchestratedMainInput(pending, "uncertain", "", err)
			} else if !u.appServerInputOperation.pending() {
				pending.dispatched = false
				u.retainOrchestratedMainInput(pending, "rejected", "", errors.New("coordinator composer rejected input: "+u.notice))
			}
		}
	})
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
	if pending.command.reply != nil {
		pending.command.reply <- orchestrateResult{delivery: &d, err: err}
	} else if err != nil {
		u.setNotice("Orchestration input: "+err.Error(), true)
	}
	u.orchestrateMainInput = nil
	if err := u.flushInput(); err != nil {
		u.setNotice(err.Error(), true)
	}
}

func (u *appServerUI) retainOrchestratedMainInput(pending *orchestrateMainInput, state, turn string, outcome error) {
	if pending.delivery.ID == "" {
		u.finishOrchestratedMainInput(pending, pending.delivery, outcome)
		return
	}
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
