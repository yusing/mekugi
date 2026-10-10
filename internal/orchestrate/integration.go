package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Integration records observed native ancestry, not Main's review or acceptance.
type Integration struct {
	Tip       string `json:"tip"`
	SourceTip string `json:"source_tip"`
}

func validateIndexFlags(ctx context.Context, repository string) error {
	files, err := git(ctx, repository, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(files, "\x00") {
		if len(entry) != 0 && (entry[0] == 'S' || entry[0] >= 'a' && entry[0] <= 'z') {
			return fmt.Errorf("checkout %q has assume-unchanged or skip-worktree paths; inspect these paths first", repository)
		}
	}
	return nil
}

// RecordIntegration verifies integration without changing either checkout.
// The caller records journal acceptance separately after its lifecycle checks.
func (s *Store) RecordIntegration(ctx context.Context, workspace, main, name, thread string) (proof Integration, err error) {
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil || b.Launch.ThreadID != thread || thread == "" {
			return errors.New("integration requires a confirmed batch thread")
		}
		if b.VCS == "shadow" {
			return errors.New("shadow integration is not yet available")
		}
		if err := validateCheckoutIdentity(ctx, *b); err != nil {
			return err
		}
		if err := validateSubmodules(ctx, *b, false); err != nil {
			return err
		}
		for _, sub := range b.Submodules {
			if err := validateIndexFlags(ctx, filepath.Join(b.Checkout, sub.Path)); err != nil {
				return err
			}
			status, err := git(ctx, filepath.Join(b.Checkout, sub.Path), "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
			if err != nil {
				return err
			}
			if status != "" {
				return errors.New("integration requires clean submodule checkouts")
			}
		}
		if err := validateIndexFlags(ctx, b.Checkout); err != nil {
			return err
		}
		status, err := git(ctx, b.Cwd, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
		if err != nil {
			return err
		}
		if status != "" {
			return errors.New("integration requires a clean batch checkout")
		}
		proof.Tip, err = git(ctx, b.Cwd, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		proof.SourceTip, err = git(ctx, workspace, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		if _, err := git(ctx, workspace, "merge-base", "--is-ancestor", proof.Tip, proof.SourceTip); err != nil {
			return errors.New("batch tip is not integrated into source HEAD")
		}
		b.Integration = &proof
		return nil
	})
	return proof, err
}
