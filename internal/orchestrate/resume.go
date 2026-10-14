package orchestrate

import (
	"context"
	"errors"
)

// Resume reads retained identity without changing launch facts or batch files.
func (s *Store) Resume(ctx context.Context, workspace, main, name string) (Batch, error) {
	return s.resume(ctx, workspace, main, name, false)
}

// ResumeCleanup admits retained removal intent for host idle checks. Cleanup
// owns filesystem reconciliation; this read cannot authorize another removal.
func (s *Store) ResumeCleanup(ctx context.Context, workspace, main, name string) (Batch, error) {
	return s.resume(ctx, workspace, main, name, true)
}

func (s *Store) resume(ctx context.Context, workspace, main, name string, cleanup bool) (Batch, error) {
	var batch Batch
	err := s.withBatchRun(ctx, workspace, main, name, func(m *manifest, _ string) error {
		for _, b := range m.Batches {
			if b.TaskName != name {
				continue
			}
			batch = b
			if b.Launch == nil || b.Launch.ThreadID == "" || b.State == "removing" && !cleanup {
				return errors.New("batch has no resumable confirmed thread")
			}
			if b.VCS != "" && !shadowVCS(b.VCS) {
				return errors.New("unsupported checkout VCS")
			}
			if b.State == "removed" || b.State == "removing" {
				return nil // Retained history stays viewable, with input disabled.
			}
			if err := validateCheckoutIdentity(ctx, b); err != nil {
				return err
			}
			if b.VCS == "" {
				repository, err := sourceCheckout(ctx, workspace, b)
				if err != nil {
					return err
				}
				source, err := git(ctx, repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil {
					return err
				}
				child, err := git(ctx, b.Checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
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
