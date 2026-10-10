package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// The closest VCS metadata wins; excluded VCS cannot become unversioned shadows.
func sourceRepository(ctx context.Context, workspace string) (string, string, error) {
	selected, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", err
	}
	for dir := selected; ; dir = filepath.Dir(dir) {
		for _, marker := range []string{".jj", ".hg", ".svn", ".bzr", ".git"} {
			_, err := os.Lstat(filepath.Join(dir, marker))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", "", err
			}
			switch marker {
			case ".jj", ".hg", ".bzr":
				return "", "", errors.New("orchestration supports Git, SVN and unversioned shadow only")
			case ".svn":
				return "", "", errors.New("SVN committed baselines are not yet available")
			case ".git":
				root, err := git(ctx, selected, "rev-parse", "--show-toplevel")
				return "", root, err
			}
		}
		if filepath.Dir(dir) == dir {
			return "shadow", selected, nil
		}
	}
}

func sourceBase(ctx context.Context, cwd string) (string, error) {
	return git(ctx, cwd, "rev-parse", "--verify", "HEAD^{commit}")
}

func committedDirectory(ctx context.Context, repository, base, relative string) error {
	kind, err := git(ctx, repository, "cat-file", "-t", base+":"+filepath.ToSlash(relative))
	if err != nil {
		return err
	}
	if kind != "tree" {
		return errors.New("committed path is not a directory")
	}
	return nil
}

func createCheckout(ctx context.Context, repository string, b Batch) error {
	_, err := git(ctx, repository, "worktree", "add", "--quiet", "-b", b.Branch, "--", b.Checkout, b.Base)
	return err
}
