package orchestrate

import (
	"context"
	"errors"
)

// Resume reads retained identity without changing launch facts or batch files.
func (s *Store) Resume(ctx context.Context, workspace, main, name string) (Batch, error) {
	var batch Batch
	err := s.withRun(ctx, workspace, main, func(m *manifest, _ string) error {
		for _, b := range m.Batches {
			if b.TaskName != name {
				continue
			}
			batch = b
			if b.Launch == nil || b.Launch.ThreadID == "" || b.State != "launched" && b.State != "started" && b.State != "removed" {
				return errors.New("batch has no resumable confirmed thread")
			}
			if b.VCS != "" && !shadowVCS(b.VCS) {
				return errors.New("unsupported checkout VCS")
			}
			if b.State == "removed" {
				return nil // Retained history stays viewable, with input disabled.
			}
			if err := validateCheckoutIdentity(ctx, b); err != nil {
				return err
			}
			if b.VCS == "" {
				source, err := git(ctx, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil {
					return err
				}
				child, err := git(ctx, b.Cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil || source != child {
					return errors.New("retained checkout repository changed")
				}
			}
			return nil
		}
		return errors.New("batch is not in this run")
	})
	return batch, err
}
