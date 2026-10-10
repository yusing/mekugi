package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RetainedSubmodule names a source ref that reaches a changed batch gitlink.
type RetainedSubmodule struct {
	Path string `json:"path"`
	Tip  string `json:"tip"`
	Ref  string `json:"ref"`
}

func cleanupGitlinks(ctx context.Context, repository, revision string) (map[string]string, error) {
	tree, err := git(ctx, repository, "ls-tree", "-rz", "--full-tree", revision)
	if err != nil {
		return nil, err
	}
	links := make(map[string]string)
	for entry := range strings.SplitSeq(tree, "\x00") {
		if !strings.HasPrefix(entry, "160000 commit ") {
			continue
		}
		tip, path, ok := strings.Cut(strings.TrimPrefix(entry, "160000 commit "), "\t")
		if !ok || !filepath.IsLocal(path) {
			return nil, errors.New("invalid committed submodule path")
		}
		links[path] = tip
	}
	return links, nil
}

// Validate all clones before retaining refs or authorizing forced removal.
func inspectCleanupSubmodules(ctx context.Context, b Batch) ([]RetainedSubmodule, error) {
	if err := validateSubmodules(ctx, b, false); err != nil {
		return nil, err
	}
	owned := map[string]bool{filepath.Join(b.Checkout, ".git"): true}
	for _, sub := range b.Submodules {
		owned[filepath.Join(b.Checkout, sub.Path, ".git")] = true
	}
	if err := filepath.WalkDir(b.Checkout, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if !owned[path] || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("unowned repository storage %q; preserve the checkout", path)
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	parents := append([]Submodule{{Repository: b.Checkout, Base: b.Base}}, b.Submodules...)
	var retained []RetainedSubmodule
	seen := make(map[string]bool)
	for _, parent := range parents {
		repository := filepath.Join(b.Checkout, parent.Path)
		if err := validateIndexFlags(ctx, repository); err != nil {
			return nil, err
		}
		gitdir, err := git(ctx, repository, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, err
		}
		// Prepared clones use inline .git directories. Absorbed, deinitialized
		// or linked repositories are outside the inspected ownership set.
		for _, name := range []string{"modules", "worktrees"} {
			storage := filepath.Join(gitdir, name)
			info, err := os.Lstat(storage)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("uninspected repository storage %q", storage)
			}
			entries, err := os.ReadDir(storage)
			if err != nil || len(entries) != 0 {
				return nil, fmt.Errorf("uninspected repository storage %q; preserve the checkout", storage)
			}
		}
		links, err := cleanupGitlinks(ctx, repository, "HEAD")
		if err != nil {
			return nil, err
		}
		before, err := cleanupGitlinks(ctx, repository, parent.Base)
		if err != nil {
			return nil, err
		}
		var declared []string
		if len(links) != 0 {
			config, err := git(ctx, repository, "config", "--blob", "HEAD:.gitmodules", "--null", "--list")
			if err != nil {
				return nil, fmt.Errorf("submodule declarations unavailable: %w", err)
			}
			for entry := range strings.SplitSeq(config, "\x00") {
				key, value, _ := strings.Cut(entry, "\n")
				if strings.HasPrefix(key, "submodule.") && strings.HasSuffix(key, ".path") {
					declared = append(declared, value)
				}
			}
		}
		for path, tip := range links {
			full := filepath.Join(parent.Path, path)
			index := slices.IndexFunc(b.Submodules, func(sub Submodule) bool { return sub.Path == full })
			if index < 0 {
				if before[path] != tip {
					return nil, fmt.Errorf("changed submodule %q is not a run-owned clone", full)
				}
				continue // An unchanged, uninitialized submodule has no owned repository.
			}
			if !slices.Contains(declared, path) {
				return nil, fmt.Errorf("submodule %q has no committed .gitmodules entry", full)
			}
			clone := filepath.Join(b.Checkout, full)
			head, err := sourceBase(ctx, clone)
			if err != nil || head != tip {
				return nil, fmt.Errorf("submodule %q is not at its committed gitlink", full)
			}
			status, err := git(ctx, clone, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
			if err != nil || status != "" {
				return nil, fmt.Errorf("submodule %q has uncommitted files", full)
			}
			stash, err := git(ctx, clone, "for-each-ref", "--format=%(refname)", "refs/stash")
			if err != nil || stash != "" {
				return nil, fmt.Errorf("submodule %q has a stash or unreadable refs", full)
			}
			unique, err := git(ctx, clone, "rev-list", "--max-count=1", "--branches", "--tags", "--not", "--remotes", tip, b.Submodules[index].Base)
			if err != nil || unique != "" {
				return nil, fmt.Errorf("submodule %q has unique branch or tag work", full)
			}
			seen[full] = true
			if tip != b.Submodules[index].Base {
				retained = append(retained, RetainedSubmodule{Path: full, Tip: tip})
			}
		}
	}
	if len(seen) != len(b.Submodules) {
		return nil, errors.New("recorded submodule was removed or deinitialized; preserve its repository")
	}
	slices.SortFunc(retained, func(a, b RetainedSubmodule) int { return strings.Compare(a.Path, b.Path) })
	return retained, nil
}

func retainCleanupSubmodules(ctx context.Context, b Batch) ([]RetainedSubmodule, error) {
	retained, err := inspectCleanupSubmodules(ctx, b)
	if err != nil {
		return nil, err
	}
	// Innermost commits survive first, before their parent repositories disappear.
	for i := len(retained) - 1; i >= 0; i-- {
		item := &retained[i]
		sub := b.Submodules[slices.IndexFunc(b.Submodules, func(sub Submodule) bool { return sub.Path == item.Path })]
		resolved, err := filepath.EvalSymlinks(sub.Repository)
		if err != nil || resolved != sub.Repository {
			return nil, fmt.Errorf("submodule %q source repository changed", sub.Path)
		}
		root, err := git(ctx, sub.Repository, "rev-parse", "--show-toplevel")
		if err != nil || root != sub.Repository {
			return nil, fmt.Errorf("submodule %q source is not initialized", sub.Path)
		}
		if _, err := git(ctx, sub.Repository, "--no-lazy-fetch", "cat-file", "-e", sub.Base+"^{commit}"); err != nil {
			return nil, fmt.Errorf("submodule %q source baseline is unavailable: %w", sub.Path, err)
		}
		refs, err := git(ctx, sub.Repository, "--no-lazy-fetch", "for-each-ref", "--contains", item.Tip, "--count=1", "--format=%(refname)")
		if err == nil && refs != "" {
			item.Ref = refs
			continue
		}
		item.Ref = "refs/mekugi/orchestrate/" + b.Branch
		// An existing different ref is never overwritten, even by a fast-forward.
		existing, err := git(ctx, sub.Repository, "for-each-ref", "--format=%(objectname)", item.Ref)
		if err != nil || existing != "" && existing != item.Tip {
			return nil, fmt.Errorf("submodule %q retention ref is unavailable or occupied", sub.Path)
		}
		if _, err := git(ctx, sub.Repository, "--no-lazy-fetch", "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--", filepath.Join(b.Checkout, sub.Path), item.Tip); err != nil {
			return nil, fmt.Errorf("retain submodule %q: %w", sub.Path, err)
		}
		if _, err := git(ctx, sub.Repository, "update-ref", item.Ref, item.Tip, ""); err != nil {
			return nil, fmt.Errorf("retain submodule %q without replacing an existing ref: %w", sub.Path, err)
		}
	}
	return retained, nil
}
