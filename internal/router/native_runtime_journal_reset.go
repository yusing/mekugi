package router

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/yusing/mekugi/internal/session"
)

func (u *appServerUI) runtimeJournalOwner() *runtimeJournalOwner {
	if u.runtime.observations == nil {
		return nil
	}
	return u.runtime.observations.journal
}

func (u *appServerUI) runtimeJournalScope() (*runtimeJournalOwner, context.Context, ObservationBinding, error) {
	o := u.runtimeJournalOwner()
	if o == nil {
		return nil, u.ctx, ObservationBinding{}, errors.New("native journal unavailable")
	}
	b := o.rootBinding()
	ctx, err := o.scope(u.ctx, b)
	return o, ctx, b, err
}

func (u *appServerUI) beginRuntimeJournalTurn() error {
	u.runtime.turn = "native-turn-" + rand.Text()
	if o := u.runtimeJournalOwner(); o != nil {
		return o.startTurn(u.ctx, u.runtime.turn)
	}
	return nil
}

func (u *appServerUI) stopRuntimeJournalTurn() error {
	o := u.runtimeJournalOwner()
	if o == nil {
		return nil
	}
	return o.stopTurn(u.ctx, u.runtime.turn)
}

func (u *appServerUI) completedRuntimeJournal() error {
	if u.runtimeJournalOwner() == nil || u.runtime.turn == "" {
		return nil
	}
	o, ctx, b, err := u.runtimeJournalScope()
	if err != nil {
		return err
	}
	intent, err := o.journals.completedSlice(ctx, o.capture.store, b.Workspace, observationThread(b), u.runtime.turn)
	if err == nil && intent != nil {
		u.runtime.continuation, u.runtime.continueAt = intent, u.now().Add(3*time.Second)
	}
	return err
}

func (u *appServerUI) cancelRuntimeContinuation(stop bool) error {
	r := u.runtime
	if r.resetRequest != "" && stop {
		r.continuation = nil
		if err := u.runtimeJournalOwner().cancelReset(u.ctx, r.resetRequest, r.resetSource); err != nil {
			u.setNotice("Journal stop receipt unavailable: "+err.Error(), true)
		} else {
			u.setNotice("Journal reset interrupted · automatic continuation stopped", false)
		}
		return r.client.Interrupt(u.ctx)
	}
	if r.continuation == nil {
		return nil
	}
	o, ctx, b, err := u.runtimeJournalScope()
	if err != nil {
		return err
	}
	if stop {
		err = o.journals.stopJournalTurn(ctx, o.capture.store, b.Workspace, observationThread(b), r.continuation.Turn)
	} else {
		err = o.journals.changeReset(ctx, o.capture.store, b.Workspace, observationThread(b), r.continuation.ID, func(j *threadJournal, _ *journalResetIntent) error { j.ResetIntent = nil; return nil })
	}
	r.continuation = nil
	u.setNotice("Journal continuation cancelled", false)
	return err
}

func (u *appServerUI) runtimeBackgroundWork() bool {
	for _, task := range u.runtime.tasks {
		if !runtimeTaskTerminal(task.Status) {
			return true
		}
	}
	return u.runtime.observations != nil && u.runtime.observations.owner.pendingCount.Load() != 0
}

func (u *appServerUI) requestRuntimeReset() error {
	r := u.runtime
	client, ok := r.client.(session.ResetClient)
	if !ok || r.busy || !r.ready || r.resetRequest != "" || r.settings != nil || u.questionCount() != 0 || u.runtimeBackgroundWork() {
		return errors.New("reset waits for idle native work, permissions and settings")
	}
	id := "native-reset-" + rand.Text()
	r.resetSource = u.runtimeJournalOwner().rootBinding()
	if err := client.Reset(u.ctx, id); err != nil {
		return err
	}
	r.resetRequest, r.ready = id, false
	u.status = "Preparing journal context…"
	return nil
}

func (u *appServerUI) runtimeResetReady(e session.Event) error {
	r := u.runtime
	if r.resetRequest == "" || r.resetRequest != e.ID {
		return nil
	}
	r.resetRequest, r.ready = "", true
	u.status = "Ready"
	if e.Failed {
		r.continuation = nil
		u.setNotice("Journal reset unavailable: "+e.Text, true)
		return nil
	}
	// No native session has been created yet. Input will establish identity.
	r.restoredJournal = ""
	u.setNotice("Journal context prepared · next input creates the native session", false)
	if r.continuation != nil && u.draft == "" && len(u.images) == 0 && u.questionCount() == 0 {
		text := journalContinuationText(r.continuation)
		r.continuation = nil
		return u.sendRuntimeContinuation(text)
	}
	r.continuation = nil
	return nil
}

func (u *appServerUI) sendRuntimeContinuation(text string) error {
	if err := u.beginRuntimeJournalTurn(); err != nil {
		return err
	}
	if err := u.runtime.client.Send(u.ctx, text); err != nil {
		return err // Uncertain sends are never retried.
	}
	u.runtime.busy = true
	u.status = "Working"
	return nil
}

func (u *appServerUI) tickRuntimeJournal(now time.Time) error {
	r := u.runtime
	if u.runtimeJournalOwner() == nil || r.busy || !r.ready || r.resetRequest != "" {
		return nil
	}
	o, ctx, b, err := u.runtimeJournalScope()
	if err != nil {
		return nil // Native identity is not available before first input.
	}
	thread := observationThread(b)
	if r.restoredJournal != thread {
		r.restoredJournal = thread
		intent, interrupted, err := o.journals.restorableReset(ctx, o.capture.store, b.Workspace, thread)
		if err != nil {
			return err
		}
		if interrupted {
			u.setNotice("Journal continuation was interrupted; continue manually", true)
		}
		if intent != nil {
			r.continuation, r.continueAt = intent, now.Add(3*time.Second)
		}
	}
	if r.continuation == nil {
		return nil
	}
	u.dirty = true
	if u.draft != "" || len(u.images) != 0 || u.questionCount() > 0 {
		return u.cancelRuntimeContinuation(false)
	}
	if now.Before(r.continueAt) || r.settings != nil || u.runtimeBackgroundWork() {
		return nil
	}
	intent := r.continuation
	if err := o.journals.changeReset(ctx, o.capture.store, b.Workspace, thread, intent.ID, func(j *threadJournal, current *journalResetIntent) error {
		if !j.continuationCurrent(current) {
			return errors.New("journal work is no longer runnable")
		}
		current.Phase = "starting"
		return nil
	}); err != nil {
		r.continuation = nil
		return err
	}
	if !intent.Resume {
		if err := u.requestRuntimeReset(); err != nil {
			r.continuation = nil
			return err
		}
		return nil
	}
	r.continuation = nil
	return u.sendRuntimeContinuation(journalContinuationText(intent))
}

func (u *appServerUI) runtimeJournalLabel(now time.Time) string {
	r := u.runtime
	if r.resetRequest != "" {
		return "↻ Preparing journal context"
	}
	if r.continuation == nil {
		return ""
	}
	if u.runtimeBackgroundWork() {
		return "Journal continuation waits for native background work"
	}
	return fmt.Sprintf("Continue %s in %ds · Esc cancels", r.continuation.Title, max(0, int((r.continueAt.Sub(now)+time.Second-1)/time.Second)))
}
