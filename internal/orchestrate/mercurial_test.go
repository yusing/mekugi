package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hgTestRepository(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	if _, err := exec.Command("hg", "version").Output(); err != nil {
		t.Skip("Mercurial is unavailable:", err)
	}
	t.Setenv("HGRCPATH", os.DevNull)
	run := func(cwd string, args ...string) string {
		t.Helper()
		output, err := hg(t.Context(), cwd, args...)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	repo := t.TempDir()
	run(repo, "init")
	if err := os.Mkdir(filepath.Join(repo, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"src/file": "base", ".hgignore": "syntax: glob\ninput\n"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run(repo, "add")
	run(repo, "commit", "-u", "test", "-m", "base")
	run(repo, "bookmark", "source")
	return repo, run
}

func TestMercurialBatchLifecycle(t *testing.T) {
	repo, run := hgTestRepository(t)
	shared := filepath.Join(t.TempDir(), "source")
	run(repo, "share", "-U", "-B", repo, shared)
	run(shared, "update", "source")
	repo = shared
	workspace := filepath.Join(repo, "src")
	if err := os.WriteFile(filepath.Join(workspace, "file"), []byte("source edit"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Directory: t.TempDir()}
	first, err := store.Prepare(t.Context(), workspace, "main", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Prepare(t.Context(), workspace, "main", "second")
	if err != nil || first.VCS != "hg" || first.Checkout == second.Checkout || first.Base != second.Base {
		t.Fatal("isolated preparation failed", first, second, err)
	}
	if got, err := os.ReadFile(filepath.Join(first.Cwd, "file")); err != nil || string(got) != "base" {
		t.Fatal("source edits followed into checkout", string(got), err)
	}
	if run(repo, "bookmarks", "-T", "{if(active,bookmark)}") != "source" {
		t.Fatal("preparation changed source bookmark")
	}
	if err := os.WriteFile(filepath.Join(first.Cwd, "input"), []byte("prepared input"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened := &Store{Directory: store.Directory}
	repeat, err := reopened.Prepare(t.Context(), workspace, "main", "first")
	if err != nil || repeat != first {
		t.Fatal("retained preparation changed", repeat, err)
	}
	if _, dispatch, err := reopened.BeginLaunch(t.Context(), workspace, "main", "first", "assignment", []byte("{}")); err != nil || !dispatch {
		t.Fatal("fresh launch failed", err)
	}
	if err := reopened.RecordThread(t.Context(), workspace, "main", "first", "child", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Cwd, "result"), []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RecordIntegration(t.Context(), workspace, "main", "first", "child"); err == nil {
		t.Fatal("accepted dirty child")
	}
	run(first.Cwd, "add", "result")
	run(first.Cwd, "commit", "-u", "test", "-m", "result")
	if _, err := reopened.RecordIntegration(t.Context(), workspace, "main", "first", "child"); err == nil {
		t.Fatal("accepted unintegrated child")
	}
	run(repo, "update", "-r", first.Branch)
	proof, err := reopened.RecordIntegration(t.Context(), workspace, "main", "first", "child")
	if err != nil || proof.Tip == "" || proof.Tip != proof.SourceTip {
		t.Fatal("native integration failed", proof, err)
	}
	for path, want := range map[string]string{filepath.Join(workspace, "file"): "source edit", filepath.Join(first.Cwd, "input"): "prepared input"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatal("owned input changed", path, err)
		}
	}
	batches, err := (&Store{Directory: store.Directory}).Snapshot(workspace, "main")
	if err != nil || batches[0].Integration == nil || *batches[0].Integration != proof {
		t.Fatal("integration lost on restart", batches, err)
	}
	// Another active bookmark cannot authorize launch of the retained batch.
	run(second.Cwd, "bookmark", "other")
	if _, dispatch, err := store.BeginLaunch(t.Context(), workspace, "main", "second", "assignment", []byte("{}")); err == nil || dispatch {
		t.Fatal("changed bookmark authorized launch")
	}
	branch := strings.TrimSuffix(first.Branch, "/first") + "/failed"
	run(repo, "bookmark", branch)
	failed, err := store.Prepare(t.Context(), workspace, "main", "failed")
	if err == nil || failed.State != "failed" {
		t.Fatal("failed preparation lost intent", failed, err)
	}
	if _, err := reopened.Prepare(t.Context(), workspace, "main", "failed"); err == nil {
		t.Fatal("failed effect repeated")
	}
}
