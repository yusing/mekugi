package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The private run directory, not the checkout or a caller-supplied path, owns
// reviewer scratch. Publish the allocated directory before starting the host.
func (s *Store) PrepareWorkflowReviewStorage(ctx context.Context, workspace, main, name, id string) (scratch string, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.TaskName != name {
				continue
			}
			w, err := workflowFor(b)
			if err != nil {
				return err
			}
			for j := range w.Dispatches {
				d := &w.Dispatches[j]
				if d.ID != id {
					continue
				}
				if d.Kind != DispatchCorrectness && d.Kind != DispatchSimplification || d.State != DispatchIntent || d.ThreadID != "" || d.Scratch != "" || d.Generation != w.Generation || w.stopped() {
					return errors.New("review storage requires a fresh review dispatch")
				}
				root, err := filepath.EvalSymlinks(strings.TrimSuffix(path, ".json"))
				if err != nil {
					return err
				}
				scratch, err = os.MkdirTemp(root, ".review-")
				if err != nil {
					return err
				}
				d.Scratch = scratch
				if err := s.save(m, path); err != nil {
					return errors.Join(err, os.RemoveAll(scratch))
				}
				return nil
			}
		}
		return errors.New("review storage has no retained dispatch")
	})
	return
}

// ReleaseWorkflowReviewStorage is called by the archive owner only after the
// host confirms reviewer shutdown. It also permits cleanup after a confirmed
// pre-launch rejection. Uncertain dispatch or shutdown must retain storage.
func (s *Store) ReleaseWorkflowReviewStorage(ctx context.Context, workspace, main, name, id string) error {
	return s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for _, b := range m.Batches {
			if b.TaskName != name || b.Workflow == nil {
				continue
			}
			for _, d := range b.Workflow.Dispatches {
				if d.ID != id {
					continue
				}
				if d.Scratch == "" {
					return nil
				}
				if !(d.Archive != nil && d.Archive.State == "archived") && !(d.State == DispatchFailed && d.ThreadID == "") {
					return errors.New("review storage requires confirmed host shutdown or pre-launch rejection")
				}
				root, err := filepath.EvalSymlinks(strings.TrimSuffix(path, ".json"))
				if err != nil {
					return err
				}
				if filepath.Dir(d.Scratch) != root || !strings.HasPrefix(filepath.Base(d.Scratch), ".review-") {
					return errors.New("review storage ownership changed")
				}
				return os.RemoveAll(d.Scratch)
			}
		}
		return errors.New("review storage has no retained dispatch")
	})
}
