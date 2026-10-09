package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareGit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	run("init", "-q")
	run("config", "user.name", "test")
	run("config", "user.email", "test@example.com")
	if err := os.Mkdir(filepath.Join(repo, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "src", "file")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(file, "base")
	run("add", ".")
	run("commit", "-qm", "base")
	write(file, "staged")
	run("add", ".")
	write(file, "unstaged")
	before := run("diff", "--binary", "HEAD") + run("diff", "--cached", "--binary")
	store := &Store{Directory: t.TempDir()}
	workspace := filepath.Join(repo, "src")
	first, err := store.Prepare(t.Context(), workspace, "main", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Prepare(t.Context(), workspace, "main", "second")
	if err != nil || first.Checkout == second.Checkout || first.Branch == second.Branch || first.Base != second.Base {
		t.Fatalf("independent batches: %+v, %+v, %v", first, second, err)
	}
	content, err := os.ReadFile(filepath.Join(first.Cwd, "file"))
	if err != nil || string(content) != "base" || first.State != "prepared" {
		t.Fatalf("committed baseline: %q, %+v, %v", content, first, err)
	}
	write(filepath.Join(first.Cwd, "input"), "copied input")
	reopened := &Store{Directory: store.Directory}
	repeat, err := reopened.Prepare(t.Context(), workspace, "main", "first")
	if err != nil || repeat != first {
		t.Fatalf("repeat: %+v, %v", repeat, err)
	}
	if content, err := os.ReadFile(filepath.Join(repeat.Cwd, "input")); err != nil || string(content) != "copied input" {
		t.Fatalf("prepared input was replaced: %q, %v", content, err)
	}
	batches, err := reopened.List(t.Context(), workspace, "main")
	if err != nil || len(batches) != 2 {
		t.Fatalf("reopen list: %+v, %v", batches, err)
	}
	other, err := reopened.List(t.Context(), workspace, "other")
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-run disclosure: %+v, %v", other, err)
	}
	if after := run("diff", "--binary", "HEAD") + run("diff", "--cached", "--binary"); before != after {
		t.Fatal("source index or uncommitted edits changed")
	}
	// Failure after saving intent remains inspectable and cannot be retried.
	err = store.withRun(t.Context(), workspace, "main", func(_ *manifest, path string) error {
		blocked := filepath.Join(strings.TrimSuffix(path, ".json"), "blocked")
		if err := os.MkdirAll(blocked, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(blocked, "existing"), []byte("retain"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Prepare(t.Context(), workspace, "main", "blocked")
	if err == nil || failed.State != "failed" {
		t.Fatalf("preparation failure: %+v, %v", failed, err)
	}
	if _, err := reopened.Prepare(t.Context(), workspace, "main", "blocked"); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("failed effect repeated: %v", err)
	}
	// A retained intent cannot be repeated after an uncertain process outcome.
	err = store.withRun(t.Context(), workspace, "main", func(m *manifest, path string) error {
		m.Batches[0].State = "preparing"
		return store.save(m, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Prepare(t.Context(), workspace, "main", "first"); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("uncertain intent repeated: %v", err)
	}
}

func TestPrepareRejectsInvalidInputs(t *testing.T) {
	store := &Store{Directory: t.TempDir()}
	for _, name := range []string{"", "../escape", "two/parts", "-option"} {
		if _, err := store.Prepare(t.Context(), t.TempDir(), "main", name); err == nil {
			t.Fatalf("accepted task %q", name)
		}
	}
	if _, err := store.Prepare(t.Context(), "relative", "main", "batch"); err == nil {
		t.Fatal("accepted relative workspace")
	}
	if _, err := store.Prepare(t.Context(), t.TempDir(), "main", "batch"); err == nil {
		t.Fatal("accepted non-Git source")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.List(ctx, t.TempDir(), "main"); err == nil {
		t.Fatal("canceled read succeeded")
	}
	// Corrupt identities never authorize an existing manifest.
	workspace := t.TempDir()
	err := store.withRun(t.Context(), workspace, "main", func(m *manifest, path string) error {
		m.Main = "other"
		return store.save(m, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(t.Context(), workspace, "main"); err == nil {
		t.Fatal("accepted mismatched manifest")
	}
}

func TestPrepareWorkspaceBoundaries(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		name := "trailing_space"
		if sparse {
			name = "sparse_cwd"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo := filepath.Join(t.TempDir(), "source ")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) string {
				t.Helper()
				command := exec.Command("git", append([]string{"-C", repo}, args...)...)
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
				return string(output)
			}
			run("init", "-q")
			run("config", "user.name", "test")
			run("config", "user.email", "test@example.com")
			for _, dir := range []string{"a", "b"} {
				if err := os.Mkdir(filepath.Join(repo, dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, dir, "file"), []byte(dir), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run("add", ".")
			run("commit", "-qm", "base")
			workspace := repo
			if sparse {
				run("sparse-checkout", "init", "--cone")
				run("sparse-checkout", "set", "a")
				workspace = filepath.Join(repo, "b")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
			}
			batch, err := (&Store{Directory: t.TempDir()}).Prepare(t.Context(), workspace, "main", "batch")
			if err != nil {
				t.Fatalf("valid workspace rejected: %v", err)
			}
			if info, err := os.Stat(batch.Cwd); err != nil || !info.IsDir() {
				t.Fatalf("prepared cwd unavailable: %+v, %v", batch, err)
			}
			if err := validateLaunchCheckout(t.Context(), batch); err != nil {
				t.Fatalf("prepared checkout cannot launch: %v", err)
			}
			if sparse && run("sparse-checkout", "list") != "a\n" {
				t.Fatal("source sparse settings changed")
			}
		})
	}
}
