package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The closest native metadata wins, including jj in a colocated Git workspace.
func sourceRepository(ctx context.Context, workspace string) (string, string, error) {
	selected, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", err
	}
	for dir := selected; ; dir = filepath.Dir(dir) {
		for _, marker := range []string{".jj", ".hg", ".git"} {
			_, err := os.Lstat(filepath.Join(dir, marker))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", "", err
			}
			switch marker {
			case ".jj":
				return "", "", errors.New("jj orchestration is not yet available")
			case ".hg":
				root, err := hg(ctx, selected, "root")
				return "hg", root, err
			case ".git":
				root, err := git(ctx, selected, "rev-parse", "--show-toplevel")
				return "", root, err
			}
		}
		if filepath.Dir(dir) == dir {
			return "", "", errors.New("workspace has no supported native repository")
		}
	}
}

func sourceBase(ctx context.Context, vcs, cwd string) (string, error) {
	if vcs == "hg" {
		parents, err := hg(ctx, cwd, "parents", "--template", "{node}\n")
		if err != nil {
			return "", err
		}
		if parents == "" || strings.Contains(parents, "\n") {
			return "", errors.New("Mercurial requires one committed working-directory parent")
		}
		return parents, nil
	}
	return git(ctx, cwd, "rev-parse", "--verify", "HEAD^{commit}")
}

func committedDirectory(ctx context.Context, vcs, repository, base, relative string) error {
	if vcs == "hg" {
		files, err := hg(ctx, repository, "files", "-r", base, "--", "path:"+filepath.ToSlash(relative)+"/")
		if err != nil {
			return err
		}
		if files == "" {
			return errors.New("directory is absent from committed parent")
		}
		return nil
	}
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
	if b.VCS == "hg" {
		existing, err := hg(ctx, repository, "bookmarks", "--template", "{bookmark}\n")
		if err != nil {
			return err
		}
		if slices.Contains(strings.Split(existing, "\n"), b.Branch) {
			return errors.New("Mercurial batch bookmark already exists")
		}
		if err := os.MkdirAll(filepath.Dir(b.Checkout), 0700); err != nil {
			return err
		}
		if _, err := hg(ctx, repository, "share", "-U", "-B", "--", repository, b.Checkout); err != nil {
			return err
		}
		if _, err := hg(ctx, b.Checkout, "update", "-r", b.Base); err != nil {
			return err
		}
		_, err = hg(ctx, b.Checkout, "bookmark", "--", b.Branch)
		return err
	}
	_, err := git(ctx, repository, "worktree", "add", "--quiet", "-b", b.Branch, "--", b.Checkout, b.Base)
	return err
}

func validateHgIdentity(ctx context.Context, b Batch) error {
	root, err := hg(ctx, b.Cwd, "root")
	if err != nil || root != b.Checkout {
		return errors.New("prepared cwd no longer belongs to its Mercurial checkout")
	}
	shared, err := hg(ctx, b.Cwd, "root", "--share-source")
	if err != nil || b.Repository == "" || shared != b.Repository {
		return errors.New("prepared Mercurial shared repository changed")
	}
	bookmark, err := hg(ctx, b.Cwd, "bookmarks", "--template", "{if(active, bookmark)}")
	if err != nil || bookmark != b.Branch {
		return errors.New("prepared Mercurial bookmark changed")
	}
	parent, err := sourceBase(ctx, "hg", b.Cwd)
	if err != nil {
		return err
	}
	tip, err := hg(ctx, b.Cwd, "log", "-r", b.Branch, "--template", "{node}")
	if err != nil || tip != parent {
		return errors.New("prepared Mercurial bookmark no longer names its checkout parent")
	}
	return nil
}

func hgIntegration(ctx context.Context, workspace string, b Batch) (proof Integration, err error) {
	root, err := hg(ctx, workspace, "root", "--share-source")
	if err != nil || root != b.Repository {
		return proof, errors.New("source Mercurial repository changed")
	}
	status, err := hg(ctx, b.Cwd, "status", "-mardu")
	if err != nil {
		return proof, err
	}
	if status != "" {
		return proof, errors.New("integration requires a clean batch checkout")
	}
	proof.Tip, err = sourceBase(ctx, "hg", b.Cwd)
	if err != nil {
		return proof, err
	}
	proof.SourceTip, err = sourceBase(ctx, "hg", workspace)
	if err != nil {
		return proof, err
	}
	ancestor, err := hg(ctx, workspace, "log", "-r", "id('"+proof.Tip+"') & ancestors(id('"+proof.SourceTip+"'))", "--template", "{node}")
	if err != nil || ancestor != proof.Tip {
		return proof, errors.New("batch tip is not integrated into the source parent")
	}
	return proof, nil
}

func hg(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "hg", append([]string{"--config", "extensions.share=", "--noninteractive", "--cwd", directory}, args...)...)
	command.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == "HGPLAIN" || key == "HGPLAINEXCEPT" || key == "HGENCODING" || key == "HG_PENDING"
	})
	command.Env = append(command.Env, "HGPLAIN=1", "HGENCODING=UTF-8")
	output, err := command.Output()
	if err != nil {
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			return "", fmt.Errorf("hg %s: %w: %s", args[0], err, strings.TrimSpace(string(failure.Stderr)))
		}
		return "", fmt.Errorf("hg %s: %w", args[0], err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
