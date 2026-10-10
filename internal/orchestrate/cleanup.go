package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Cleanup consumes Main's current journal proof, supplied by the router owner.
// Removal intent prevents new deliveries and never authorizes repeating an effect.
func (s *Store) Cleanup(ctx context.Context, workspace, main, name string, proof Integration, authorize func(publishIntent func() error) error) (batch Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.TaskName != name {
				continue
			}
			batch = *b
			if b.VCS != "" || len(b.Submodules) != 0 || b.Launch == nil || b.Launch.ThreadID == "" || b.Integration == nil || *b.Integration != proof {
				return errors.New("cleanup requires an accepted Git batch without submodules")
			}
			for _, d := range m.Deliveries {
				if (d.Target == b.Launch.ThreadID || d.From == b.Launch.ThreadID) && (d.State == "queued" || d.State == "dispatching" || d.State == "uncertain") {
					return errors.New("cleanup requires settled batch deliveries")
				}
			}
			tip, err := git(ctx, workspace, "rev-parse", "--verify", "refs/heads/"+b.Branch+"^{commit}")
			if err != nil || tip != proof.Tip {
				return errors.New("cleanup branch no longer retains the accepted tip")
			}
			if b.State == "removing" || b.State == "removed" {
				if _, err := os.Lstat(b.Checkout); !errors.Is(err, os.ErrNotExist) {
					return errors.New("cleanup checkout still exists or is uncertain; inspect removal before recovery")
				}
				worktrees, err := git(ctx, workspace, "worktree", "list", "--porcelain", "-z")
				if err != nil {
					return err
				}
				if strings.Contains(worktrees, "worktree "+b.Checkout+"\x00") {
					return errors.New("cleanup checkout still has a Git worktree registration")
				}
			} else {
				if b.State != "launched" {
					return errors.New("cleanup requires a launched batch")
				}
				if err := validateCheckoutIdentity(ctx, *b); err != nil {
					return err
				}
				source, err := git(ctx, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil {
					return err
				}
				common, err := git(ctx, b.Cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
				if err != nil || common != source {
					return errors.New("cleanup checkout repository changed")
				}
				status, err := git(ctx, b.Cwd, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
				if err != nil || status != "" {
					return errors.New("cleanup requires a clean batch checkout")
				}
				head, err := sourceBase(ctx, b.Cwd)
				if err != nil || head != proof.Tip {
					return errors.New("cleanup checkout tip changed after acceptance")
				}
				tree, err := git(ctx, b.Cwd, "ls-tree", "-rz", "--full-tree", "HEAD")
				if err != nil {
					return err
				}
				for entry := range strings.SplitSeq(tree, "\x00") {
					if strings.HasPrefix(entry, "160000 commit ") {
						return errors.New("submodule checkout cleanup is not yet available")
					}
				}
				b.State, b.Error = "removing", ""
				// The journal owner rechecks current acceptance and publishes intent
				// in that transaction. The Git effect runs after its locks release.
				if err := authorize(func() error { return s.save(m, path) }); err != nil {
					return err
				}
				batch = *b
				if _, err := git(ctx, workspace, "worktree", "remove", "--", b.Checkout); err != nil {
					b.Error = err.Error()
					batch = *b
					return errors.Join(err, s.save(m, path))
				}
			}
			b.State, b.Error = "removed", ""
			if err := s.save(m, path); err != nil {
				return err
			}
			batch = *b
			return nil
		}
		return fmt.Errorf("batch %q has not been prepared", name)
	})
	return batch, err
}
