package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Submodule records a local clone's ownership before checkout effects.
type Submodule struct {
	Path       string `json:"path"`
	Repository string `json:"repository"`
	Base       string `json:"base"`
}

func planSubmodules(ctx context.Context, source, base, prefix string) ([]Submodule, error) {
	args := []string{"ls-tree", "-rz", "--full-tree", base}
	if prefix != "" {
		args = append([]string{"--no-lazy-fetch"}, args...)
	}
	tree, err := git(ctx, source, args...)
	if err != nil {
		return nil, err
	}
	var plan []Submodule
	for entry := range strings.SplitSeq(tree, "\x00") {
		if !strings.HasPrefix(entry, "160000 commit ") {
			continue
		}
		object, path, ok := strings.Cut(strings.TrimPrefix(entry, "160000 commit "), "\t")
		if !ok || !filepath.IsLocal(path) {
			return nil, errors.New("invalid submodule path in committed tree")
		}
		repository := filepath.Join(source, path)
		if _, err := os.Lstat(filepath.Join(repository, ".git")); errors.Is(err, os.ErrNotExist) {
			continue // Preserve uninitialized submodules; never consult their URLs.
		} else if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(repository)
		if err != nil || resolved != repository {
			return nil, fmt.Errorf("submodule %q source is redirected", path)
		}
		root, err := git(ctx, repository, "rev-parse", "--show-toplevel")
		if err != nil || root != repository {
			return nil, fmt.Errorf("submodule %q local repository is unavailable", path)
		}
		if _, err := git(ctx, repository, "--no-lazy-fetch", "cat-file", "-e", object+"^{commit}"); err != nil {
			return nil, fmt.Errorf("submodule %q baseline is unavailable locally: %w", path, err)
		}
		item := Submodule{Path: filepath.Join(prefix, path), Repository: repository, Base: object}
		plan = append(plan, item)
		nested, err := planSubmodules(ctx, repository, object, item.Path)
		if err != nil {
			return nil, err
		}
		plan = append(plan, nested...)
	}
	return plan, nil
}

func createSubmodules(ctx context.Context, b Batch) error {
	for _, sub := range b.Submodules {
		checkout := filepath.Join(b.Checkout, sub.Path)
		// --local bypasses URL transport. Copy objects and dissociate alternates
		// so source maintenance cannot remove the batch's dependencies.
		if _, err := git(ctx, b.Checkout, "--no-lazy-fetch", "clone", "--quiet", "--local", "--no-hardlinks", "--dissociate", "--no-checkout", "--no-recurse-submodules", "--template=", "--", sub.Repository, checkout); err != nil {
			return fmt.Errorf("prepare submodule %q: %w", sub.Path, err)
		}
		if _, err := git(ctx, checkout, "--no-lazy-fetch", "checkout", "--quiet", "-b", b.Branch, sub.Base); err != nil {
			return fmt.Errorf("checkout submodule %q: %w", sub.Path, err)
		}
	}
	return nil
}

func validateSubmodules(ctx context.Context, b Batch, baseline bool) error {
	for _, sub := range b.Submodules {
		if !filepath.IsLocal(sub.Path) {
			return errors.New("retained submodule path is invalid")
		}
		checkout := filepath.Join(b.Checkout, sub.Path)
		common, err := git(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || common != filepath.Join(checkout, ".git") {
			return fmt.Errorf("submodule %q repository changed", sub.Path)
		}
		child := Batch{Checkout: checkout, Cwd: checkout, Branch: b.Branch, Base: sub.Base}
		if err := validateCheckoutIdentity(ctx, child); err != nil {
			return fmt.Errorf("submodule %q: %w", sub.Path, err)
		}
		if baseline {
			tip, err := sourceBase(ctx, "", checkout)
			if err != nil || tip != sub.Base {
				return fmt.Errorf("submodule %q baseline changed", sub.Path)
			}
		}
	}
	return nil
}
