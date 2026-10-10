// Package orchestrate owns durable batch identities and isolated checkouts.
package orchestrate

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/internal/persistence"
)

type Store struct {
	Directory      string
	Writes         *persistence.Counter
	ShadowSnapshot func(context.Context, string, string) (string, error)
}

type Batch struct {
	VCS                string              `json:"vcs,omitempty"`
	Repository         string              `json:"repository,omitempty"`
	TaskName           string              `json:"task_name"`
	Branch             string              `json:"branch"`
	Checkout           string              `json:"checkout"`
	Cwd                string              `json:"cwd"`
	Base               string              `json:"base"`
	State              string              `json:"state"`
	Error              string              `json:"error,omitempty"`
	Launch             *Launch             `json:"launch,omitempty"`
	Integration        *Integration        `json:"integration,omitempty"`
	Submodules         []Submodule         `json:"submodules,omitempty"`
	RetainedSubmodules []RetainedSubmodule `json:"retained_submodules,omitempty"`
	Evidence           []Evidence          `json:"evidence,omitempty"`
}

type manifest struct {
	Version    int        `json:"version"`
	Workspace  string     `json:"workspace"`
	Main       string     `json:"main"`
	Batches    []Batch    `json:"batches"`
	Deliveries []Delivery `json:"deliveries,omitempty"`
}

var taskName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (s *Store) runPath(workspace, main string) (string, error) {
	if !filepath.IsAbs(s.Directory) || !filepath.IsAbs(workspace) || main == "" {
		return "", errors.New("orchestration requires absolute storage and workspace paths and a main thread")
	}
	return filepath.Join(s.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(workspace))), fmt.Sprintf("%x.json", sha256.Sum256([]byte(main)))), nil
}

// Snapshot reads atomically published facts without waiting for a run lock.
// It grants no dispatch and performs no checkout effects.
func (s *Store) Snapshot(workspace, main string) ([]Batch, error) {
	path, err := s.runPath(workspace, main)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("read orchestration manifest: %w", err)
	}
	if m.Version != 1 || m.Workspace != workspace || m.Main != main {
		return nil, errors.New("orchestration manifest identity mismatch")
	}
	return m.Batches, nil
}

// withRun serializes separate router processes without holding replay-store locks.
func (s *Store) withRun(ctx context.Context, workspace, main string, apply func(*manifest, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.runPath(workspace, main)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer lock.Unlock()
	m := manifest{Version: 1, Workspace: workspace, Main: main}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("read orchestration manifest: %w", err)
		}
		if m.Version != 1 || m.Workspace != workspace || m.Main != main {
			return errors.New("orchestration manifest identity mismatch")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return apply(&m, path)
}

func (s *Store) save(m *manifest, path string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return persistence.AtomicFile(path, ".orchestrate-*", data, s.Writes)
}

// List returns retained preparation facts, not a live filesystem assessment.
func (s *Store) List(ctx context.Context, workspace, main string) (batches []Batch, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, _ string) error {
		batches = slices.Clone(m.Batches)
		return nil
	})
	return batches, err
}

// Prepare never retries an uncertain effect or replaces an existing checkout.
func (s *Store) Prepare(ctx context.Context, workspace, main, name string) (batch Batch, err error) {
	if !taskName.MatchString(name) {
		return batch, errors.New("task_name must match [a-z][a-z0-9_]{0,63}")
	}
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for _, existing := range m.Batches {
			if existing.TaskName == name {
				batch = existing
				if batch.VCS != "" && batch.VCS != "shadow" {
					return errors.New("retained batch uses an unsupported orchestration VCS")
				}
				if batch.State != "prepared" {
					return fmt.Errorf("batch %s is %s; inspect its retained checkout before recovery", name, batch.State)
				}
				return nil
			}
		}
		vcs, repository, err := sourceRepository(ctx, workspace)
		if err != nil {
			return err
		}
		selected, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, selected)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("selected workspace is outside its repository")
		}
		storage, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return err
		}
		inside, err := filepath.Rel(repository, storage)
		if err != nil || (inside != ".." && !strings.HasPrefix(inside, ".."+string(filepath.Separator))) {
			return errors.New("orchestration storage must be outside the source repository")
		}
		var base string
		if vcs == "shadow" {
			if s.ShadowSnapshot == nil {
				return errors.New("shadow snapshot owner is unavailable")
			}
		} else {
			base, err = sourceBase(ctx, workspace)
			if err != nil {
				return err
			}
		}
		if relative != "." {
			if err := committedDirectory(ctx, repository, base, relative); err != nil {
				return errors.New("selected workspace directory is absent from the committed baseline")
			}
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace+"\x00"+main)))
		checkout := filepath.Join(storage, strings.TrimSuffix(filepath.Base(path), ".json"), name)
		batch = Batch{TaskName: name, Branch: "mekugi/" + id + "/" + name, Checkout: checkout,
			Cwd: filepath.Join(checkout, relative), Base: base, State: "preparing"}
		if vcs == "shadow" {
			batch.VCS, batch.Repository = vcs, filepath.Join(filepath.Dir(checkout), "shadow.git")
		}
		if vcs == "" {
			batch.Submodules, err = planSubmodules(ctx, repository, base, "")
			if err != nil {
				return err
			}
		}
		m.Batches = append(m.Batches, batch)
		if err := s.save(m, path); err != nil {
			return err
		}
		var effectErr error
		if vcs == "shadow" {
			batch.Base, effectErr = s.ShadowSnapshot(ctx, selected, batch.Repository)
			if effectErr == nil {
				m.Batches[len(m.Batches)-1] = batch
				if err := s.save(m, path); err != nil {
					return err
				}
				repository = batch.Repository
			}
		}
		if effectErr == nil {
			effectErr = createCheckout(ctx, repository, batch)
		}
		if effectErr == nil {
			effectErr = createSubmodules(ctx, batch)
		}
		if effectErr == nil {
			info, err := os.Stat(batch.Cwd)
			if err != nil {
				effectErr = fmt.Errorf("prepared cwd is unavailable: %w", err)
			} else if !info.IsDir() {
				effectErr = errors.New("prepared cwd is not a directory")
			}
		}
		if effectErr != nil {
			batch.State, batch.Error = "failed", effectErr.Error()
		} else {
			batch.State = "prepared"
		}
		m.Batches[len(m.Batches)-1] = batch
		return errors.Join(effectErr, s.save(m, path))
	})
	return batch, err
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	// Materialize a full worktree without changing the source's sparse settings.
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull,
		"-c", "submodule.recurse=false", "-c", "core.sparseCheckout=false", "-C", directory}, args...)...)
	// A parent shell's Git location must not override the authenticated workspace.
	command.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == "GIT_DIR" || key == "GIT_WORK_TREE" || key == "GIT_INDEX_FILE" || key == "GIT_COMMON_DIR"
	})
	output, err := command.Output()
	if err != nil {
		var failure *exec.ExitError
		if errors.As(err, &failure) {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(failure.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}
