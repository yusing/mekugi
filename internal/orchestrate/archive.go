package orchestrate

import (
	"context"
	"errors"
	"slices"
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

func (s *Store) BeginArchive(ctx context.Context, workspace, main, name string, authorize func(func() error) error) (batch Batch, err error) {
	err = s.withBatchLocks(ctx, workspace, main, []string{name}, func() error {
		batches, err := s.Snapshot(workspace, main)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(batches, func(b Batch) bool { return b.TaskName == name })
		if i < 0 {
			return errors.New("archive target is not in this run")
		}
		batch = batches[i]
		if batch.State != "removed" || batch.Launch == nil || batch.Launch.ThreadID == "" || batch.Launch.ThreadID == main || batch.Integration == nil {
			return errors.New("archive requires the accepted, removed child batch")
		}
		if batch.Archive != nil && batch.Archive.State != "not_sent" {
			if batch.Archive.State == "archived" {
				return authorize(nil)
			}
			return errors.New("child archive is unconfirmed; inspect host archive state before recovery")
		}
		// Like cleanup, acquire journal authorization before the run lock.
		// The batch lock keeps the checked identity stable through publication.
		return authorize(func() error {
			return s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
				i := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.TaskName == name })
				if i < 0 {
					return errors.New("archive target is not in this run")
				}
				m.Batches[i].Archive = &Archive{State: "dispatching"}
				if err := s.save(m, path); err != nil {
					return err
				}
				batch = m.Batches[i]
				return nil
			})
		})
	})
	return batch, err
}

// dispatched includes failed sends when delivery to the host is uncertain.
func (s *Store) RecordArchive(ctx context.Context, workspace, main, name string, dispatched bool, hostErr error) (batch Batch, err error) {
	err = s.withBatchRun(ctx, workspace, main, name, func(m *manifest, path string) error {
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
