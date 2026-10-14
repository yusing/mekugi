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
	// Keep our metadata walk within Git's discovery boundary too. Reviewer
	// temporary fixtures must not inherit a repository above their scratch.
	ceilings := make(map[string]bool)
	for _, path := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		if filepath.IsAbs(path) {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				ceilings[resolved] = true
			}
		}
	}
	for dir := selected; ; dir = filepath.Dir(dir) {
		// Git excludes ceiling ancestors, but still inspects the selected cwd.
		if dir != selected && ceilings[dir] {
			return "shadow", selected, nil
		}
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
				return "svn", dir, nil
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

func shadowVCS(vcs string) bool { return vcs == "shadow" || vcs == "svn" }

// A retained selected directory may have been deleted by the batch. Find its
// surviving ancestor without following dangling links or accepting a file.
func existingDirectory(path string) (string, error) {
	for {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && filepath.Dir(path) != path {
			path = filepath.Dir(path)
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(path)
		}
		if err != nil || !info.IsDir() {
			return "", errors.New("retained directory is unavailable")
		}
		return path, nil
	}
}

// Source operations use the same repository even after integration deletes the
// selected subdirectory. The run remains keyed by its original workspace.
func sourceCheckout(ctx context.Context, workspace string, b Batch) (string, error) {
	directory, err := existingDirectory(workspace)
	if err != nil {
		return "", err
	}
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	missing, err := filepath.Rel(directory, workspace)
	selected, selectedErr := filepath.Rel(root, filepath.Join(physical, missing))
	expected, expectedErr := filepath.Rel(b.Checkout, b.Cwd)
	if err != nil || selectedErr != nil || expectedErr != nil || !filepath.IsLocal(expected) || selected != expected {
		return "", errors.New("retained source no longer belongs to its repository")
	}
	return root, nil
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
