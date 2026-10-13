package orchestrate

import (
	"context"
	"errors"
	"strings"
)

type WorkflowDispatchKind string

const (
	DispatchCorrectness    WorkflowDispatchKind = "correctness"
	DispatchSimplification WorkflowDispatchKind = "simplification"
	DispatchCorrection     WorkflowDispatchKind = "correction"
)

type WorkflowDispatchState string

const (
	DispatchIntent       WorkflowDispatchState = "intent"
	DispatchAcknowledged WorkflowDispatchState = "acknowledged"
	DispatchUncertain    WorkflowDispatchState = "uncertain"
	DispatchFailed       WorkflowDispatchState = "failed"
)

type WorkflowDispatch struct {
	Archive      *Archive              `json:"archive,omitempty"`
	Scratch      string                `json:"scratch,omitempty"`
	HostStatus   string                `json:"host_status,omitempty"`
	ID           string                `json:"id"`
	Kind         WorkflowDispatchKind  `json:"kind"`
	Generation   int                   `json:"generation"`
	Revision     string                `json:"revision"`
	State        WorkflowDispatchState `json:"state"`
	ThreadID     string                `json:"thread_id,omitempty"`
	TurnID       string                `json:"turn_id,omitempty"`
	ResultItemID string                `json:"result_item_id,omitempty"`
	Reason       string                `json:"reason,omitempty"`
}

func (w *Workflow) currentDispatch(kind WorkflowDispatchKind) *WorkflowDispatch {
	for i := range w.Dispatches {
		d := &w.Dispatches[i]
		if d.Generation == w.Generation && d.Kind == kind {
			return d
		}
	}
	return nil
}

// BeginWorkflowDispatch grants dispatch only after saving a new intent. Repeats,
// including intents without acknowledgement after restart, never grant dispatch.
// The caller passes the generation it observed, preventing stale queued work.
func (s *Store) BeginWorkflowDispatch(ctx context.Context, workspace, main, name, id string, generation int, kind WorkflowDispatchKind) (record WorkflowDispatch, dispatch bool, err error) {
	if strings.TrimSpace(id) == "" {
		return record, false, errors.New("dispatch requires an identity")
	}
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, e := workflowFor(b)
		if e != nil {
			return e
		}
		for _, d := range w.Dispatches {
			if d.ID == id {
				if d.Generation != generation || d.Kind != kind {
					return errors.New("dispatch identity reused with different input")
				}
				record = d
				return errLaunchUnchanged
			}
		}
		if generation != w.Generation || w.stopped() {
			return errors.New("dispatch requires an active workflow generation")
		}
		if d := w.currentDispatch(kind); d != nil {
			record = *d
			return errLaunchUnchanged
		}
		for _, d := range w.Dispatches {
			if d.Generation == w.Generation && d.State == DispatchUncertain {
				return errors.New("uncertain dispatch requires host reconciliation")
			}
		}
		revision := ""
		switch kind {
		case DispatchCorrectness, DispatchSimplification:
			if w.ReviewWaiver != "" {
				return errors.New("further reviews were explicitly waived")
			}
			if w.Candidate == nil || w.Phase != PhaseQueuedReview && w.Phase != PhaseReview {
				return errors.New("candidate is not awaiting review")
			}
			revision = w.Candidate.Revision
			w.Phase = PhaseReview
		case DispatchCorrection:
			if w.Phase != PhaseCorrection || w.CorrectionRounds >= 2 {
				return errors.New("workflow is not awaiting correction")
			}
			if w.Candidate != nil {
				revision = w.Candidate.Revision
			}
			w.CorrectionRounds++
		default:
			return errors.New("invalid workflow dispatch kind")
		}
		record = WorkflowDispatch{ID: id, Kind: kind, Generation: generation, Revision: revision, State: DispatchIntent}
		w.Dispatches = append(w.Dispatches, record)
		dispatch = true
		return nil
	})
	if err != nil {
		dispatch = false
	}
	return
}

// RecordWorkflowDispatch saves thread acknowledgement before turn/start, then
// saves the turn acknowledgement before dependent actions. It also reconciles
// an uncertain dispatch using host identity, without granting a second launch.
// Late acknowledgements remain retained after explicit stops or revision changes.
func (s *Store) RecordWorkflowDispatch(ctx context.Context, workspace, main, name, id, thread, turn string) error {
	if strings.TrimSpace(thread) == "" {
		return errors.New("dispatch acknowledgement requires a thread")
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
			if d.State == DispatchFailed || d.ThreadID != "" && d.ThreadID != thread || d.TurnID != "" && d.TurnID != turn {
				return errors.New("dispatch acknowledgement changed")
			}
			if d.Kind == DispatchCorrection {
				if b.Launch == nil || thread != b.Launch.ThreadID {
					return errors.New("correction must use the implementer thread")
				}
			} else {
				if b.Launch == nil || thread == b.Launch.ThreadID || thread == main {
					return errors.New("review requires an independent thread")
				}
				for _, other := range w.Dispatches {
					if other.ID != id && other.ThreadID == thread {
						return errors.New("review requires a fresh independent thread")
					}
				}
			}
			if d.ThreadID == thread && d.TurnID == turn && d.State != DispatchUncertain {
				return errLaunchUnchanged
			}
			d.ThreadID, d.TurnID, d.Reason = thread, turn, ""
			d.State = DispatchIntent
			if turn != "" {
				d.State = DispatchAcknowledged
			}
			return nil
		}
		return errors.New("dispatch has no retained intent")
	})
}

// FailWorkflowDispatch retains an uncertain or rejected host effect. Only host
// reconciliation may acknowledge an uncertain effect; Begin never retries it.
func (s *Store) FailWorkflowDispatch(ctx context.Context, workspace, main, name, id string, uncertain bool, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("dispatch failure requires a reason")
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
			state := DispatchFailed
			if uncertain {
				state = DispatchUncertain
			}
			if d.State == state && d.Reason == reason {
				return errLaunchUnchanged
			}
			if d.State == DispatchAcknowledged || d.State == DispatchFailed {
				return errors.New("dispatch already settled")
			}
			d.State, d.Reason = state, reason
			if !uncertain && !w.stopped() && d.Generation == w.Generation {
				w.Phase, w.Reason = PhaseFailed, reason
			}
			return nil
		}
		return errors.New("dispatch has no retained intent")
	})
}
