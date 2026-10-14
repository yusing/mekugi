package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
)

// ObserveCandidate uses the checkout owners shared by integration, including the
// private Git commits used for SVN and unversioned batches. It changes no files.
func (s *Store) ObserveCandidate(ctx context.Context, workspace, main, name string) (candidate Candidate, err error) {
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if _, err := workflowFor(b); err != nil {
			return err
		}
		if err := validateCheckoutIdentity(ctx, *b); err != nil {
			return err
		}
		if err := validateSubmodules(ctx, *b, false); err != nil {
			return err
		}
		for _, sub := range b.Submodules {
			if err := validateCleanCheckout(ctx, filepath.Join(b.Checkout, sub.Path)); err != nil {
				return err
			}
		}
		if err := validateCleanCheckout(ctx, b.Checkout); err != nil {
			return err
		}
		candidate.Revision, err = sourceBase(ctx, b.Cwd)
		if err != nil {
			return err
		}
		if err := candidate.Validate(); err != nil {
			return err
		}
		return errLaunchUnchanged
	})
	return
}

// RecordWorkflowCompletion retains host lifecycle separately from dispatch ack.
// This permits cleanup/recovery to distinguish a stopped policy from active work.
func (s *Store) RecordWorkflowCompletion(ctx context.Context, workspace, main, name, id, thread, turn, status string) error {
	if status != "completed" && status != "failed" && status != "interrupted" {
		return errors.New("workflow completion requires a terminal host status")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		for i := range w.Dispatches {
			d := &w.Dispatches[i]
			if d.ID != id {
				continue
			}
			if d.Kind == DispatchValidation && (d.State == DispatchIntent || d.State == DispatchUncertain) && thread == "" && turn == "" {
				// A late command exit settles the host effect without approving a
				// canceled or superseded validation round.
				d.State = DispatchAcknowledged
			}
			if d.State != DispatchAcknowledged || d.ThreadID != thread || d.TurnID != turn {
				return errors.New("completion does not match acknowledged work")
			}
			if d.HostStatus == status {
				return errLaunchUnchanged
			}
			if d.HostStatus != "" {
				return errors.New("host completion changed")
			}
			d.HostStatus = status
			return nil
		}
		return errors.New("completion has no dispatch intent")
	})
}

type WorkflowOutcome struct {
	Phase   WorkflowPhase         `json:"phase"`
	Message string                `json:"message"`
	State   WorkflowDispatchState `json:"state"`
}

// BeginWorkflowOutcome reserves the single terminal notification before host
// injection. An uncertain notification is inspectable, never automatically resent.
func (s *Store) BeginWorkflowOutcome(ctx context.Context, workspace, main, name, message string) (dispatch bool, err error) {
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, e := workflowFor(b)
		if e != nil {
			return e
		}
		if w.Outcome != nil {
			return errLaunchUnchanged
		}
		if !w.stopped() && w.Phase != PhaseReady {
			return errors.New("workflow has no terminal outcome")
		}
		if message == "" {
			return errors.New("workflow outcome requires text")
		}
		w.Outcome = &WorkflowOutcome{Phase: w.Phase, Message: message, State: DispatchIntent}
		dispatch = true
		return nil
	})
	if err != nil {
		dispatch = false
	}
	return
}
func (s *Store) RecordWorkflowOutcome(ctx context.Context, workspace, main, name string, acknowledged bool) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, e := workflowFor(b)
		if e != nil {
			return e
		}
		if w.Outcome == nil {
			return errors.New("outcome has no retained intent")
		}
		state := DispatchUncertain
		if acknowledged {
			state = DispatchAcknowledged
		}
		if w.Outcome.State == state {
			return errLaunchUnchanged
		}
		if w.Outcome.State != DispatchIntent {
			return errors.New("outcome already settled")
		}
		w.Outcome.State = state
		return nil
	})
}
