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
			b.Archive = &Archive{State: "archived"}
			if !dispatched {
				b.Archive.State = "not_sent"
			} else if hostErr != nil {
				b.Archive.State = "uncertain"
			}
			if hostErr != nil {
				b.Archive.Error = hostErr.Error()
			}
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
