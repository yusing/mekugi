package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Integration records committed Git delivery or confirmed shadow writeback, not review.
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

func validateCleanCheckout(ctx context.Context, repository string) error {
	if err := validateIndexFlags(ctx, repository); err != nil {
		return err
	}
	status, err := git(ctx, repository, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("checkout %q has uncommitted files", repository)
	}
	return nil
}

// Existing history belongs to the baseline. Only added history must be linear.
func validateLinearHistory(ctx context.Context, repository, base, tip string) error {
	merges, err := git(ctx, repository, "rev-list", "--min-parents=2", "--max-count=1", base+".."+tip)
	if err != nil {
		return err
	}
	if merges != "" {
		return fmt.Errorf("repository %q has merge commits after the batch baseline; integrate linearly", repository)
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
		if shadowVCS(b.VCS) {
			vcs, source, err := sourceRepository(ctx, workspace)
			if err != nil || vcs != b.VCS || source != b.Source {
				return errors.New("shadow source identity changed")
			}
			proof, err = verifyShadowIntegration(ctx, *b)
			return err
		}
		proof.Tip, err = git(ctx, b.Cwd, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		proof.SourceTip, err = git(ctx, workspace, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		if err := validateLinearHistory(ctx, b.Cwd, b.Base, proof.Tip); err != nil {
			return err
		}
		if err := validateLinearHistory(ctx, workspace, b.Base, proof.SourceTip); err != nil {
			return err
		}
		// Follow committed source gitlinks, never the source's dirty submodule HEADs.
		parents := []Submodule{{Repository: workspace, Base: proof.SourceTip}}
		for _, sub := range b.Submodules {
			parent := parents[0]
			for _, candidate := range parents[1:] {
				if strings.HasPrefix(sub.Path, candidate.Path+string(filepath.Separator)) && len(candidate.Path) > len(parent.Path) {
					parent = candidate
				}
			}
			links, err := cleanupGitlinks(ctx, parent.Repository, parent.Base)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(parent.Path, sub.Path)
			if err != nil || links[filepath.ToSlash(relative)] == "" {
				return fmt.Errorf("source submodule %q has no committed gitlink", sub.Path)
			}
			sourceTip := links[filepath.ToSlash(relative)]
			clone := filepath.Join(b.Checkout, sub.Path)
			tip, err := sourceBase(ctx, clone)
			if err != nil {
				return err
			}
			if err := validateLinearHistory(ctx, clone, sub.Base, tip); err != nil {
				return err
			}
			repository := sub.Repository
			if sourceTip == tip {
				repository = clone // The owned clone proves this exact committed object.
			} else if err := validateLinearHistory(ctx, repository, sub.Base, sourceTip); err != nil {
				return err
			}
			parents = append(parents, Submodule{Path: sub.Path, Repository: repository, Base: sourceTip})
		}
		unmatched, err := git(ctx, workspace, "log", "--cherry-pick", "--right-only", "--format=%H", proof.SourceTip+"..."+proof.Tip)
		if err != nil {
			return err
		}
		if unmatched != "" {
			return errors.New("batch commits are not integrated into source HEAD")
		}
		b.Integration = &proof
		return nil
	})
	return proof, err
}
