package orchestrate

import (
	"context"
	"errors"
)

// Archive retains intent before the host effect. An unconfirmed effect requires
// inspection, even if the host returned an error after partially archiving.
type Archive struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func archiveOutcome(dispatched bool, hostErr error) *Archive {
	a := &Archive{State: "archived"}
	if !dispatched {
		a.State = "not_sent"
	} else if hostErr != nil {
		a.State = "uncertain"
	}
	if hostErr != nil {
		a.Error = hostErr.Error()
	}
	return a
}

// BeginReviewerArchive reserves a terminal reviewer's host effect independently
// of workflow approval. A retained attempt never grants another dispatch.
func (s *Store) BeginReviewerArchive(ctx context.Context, workspace, main, name, id string) (dispatch bool, err error) {
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Workflow == nil || b.Launch == nil {
			return errors.New("archive requires a retained reviewer")
		}
		for i := range b.Workflow.Dispatches {
			d := &b.Workflow.Dispatches[i]
			if d.ID != id {
				continue
			}
			if (d.Kind != DispatchCorrectness && d.Kind != DispatchSimplification) || d.ThreadID == "" || d.ThreadID == main || d.ThreadID == b.Launch.ThreadID || d.TurnID == "" || d.State != DispatchAcknowledged || (d.HostStatus != "completed" && d.HostStatus != "failed" && d.HostStatus != "interrupted") {
				return errors.New("archive requires an acknowledged terminal independent reviewer")
			}
			if d.ResultItemID == "" && !b.Workflow.stopped() && d.Generation == b.Workflow.Generation {
				return errors.New("archive requires retained review or stopped workflow evidence")
			}
			if d.Archive != nil {
				return errLaunchUnchanged
			}
			d.Archive = &Archive{State: "dispatching"}
			dispatch = true
			return nil
		}
		return errors.New("archive target is not a retained reviewer")
	})
	return dispatch && err == nil, err
}

func (s *Store) RecordReviewerArchive(ctx context.Context, workspace, main, name, id string, dispatched bool, hostErr error) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Workflow != nil {
			for i := range b.Workflow.Dispatches {
				d := &b.Workflow.Dispatches[i]
				if d.ID != id {
					continue
				}
				if d.Archive == nil || d.Archive.State != "dispatching" {
					return errors.New("reviewer archive has no pending intent")
				}
				d.Archive = archiveOutcome(dispatched, hostErr)
				return nil
			}
		}
		return errors.New("archive target is not a retained reviewer")
	})
}

func (s *Store) BeginArchive(ctx context.Context, workspace, main, name string, authorize func(func() error) error) (batch Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.TaskName != name {
				continue
			}
			batch = *b
			if b.State != "removed" || b.Launch == nil || b.Launch.ThreadID == "" || b.Launch.ThreadID == main || b.Integration == nil {
				return errors.New("archive requires the accepted, removed child batch")
			}
			if b.Archive != nil && b.Archive.State != "not_sent" {
				if b.Archive.State == "archived" {
					return authorize(nil)
				}
				return errors.New("child archive is unconfirmed; inspect host archive state before recovery")
			}
			b.Archive = &Archive{State: "dispatching"}
			if err := authorize(func() error { return s.save(m, path) }); err != nil {
				return err
			}
			batch = *b
			return nil
		}
		return errors.New("archive target is not in this run")
	})
	return batch, err
}

// dispatched includes failed sends when delivery to the host is uncertain.
func (s *Store) RecordArchive(ctx context.Context, workspace, main, name string, dispatched bool, hostErr error) (batch Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.TaskName != name {
				continue
			}
			if b.Archive == nil || b.Archive.State != "dispatching" {
				return errors.New("archive has no pending intent")
			}
			b.Archive = archiveOutcome(dispatched, hostErr)
			if err := s.save(m, path); err != nil {
				return err
			}
			batch = *b
			return nil
		}
		return errors.New("archive target is not in this run")
	})
	return batch, err
}
