package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotCommitHookChecksStagedTree(t *testing.T) {
	workspace := gitTestWorkspace(t)
	hooks, err := filepath.Abs("../../../../.githooks")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	makePath := filepath.Join(bin, "make")
	writeTestFile(t, makePath, `#!/bin/sh
set -eu
[ "$*" != docs-check ] || exit 0
[ "$*" = 'test-ui-snapshots SNAPSHOT_PACKAGES=@snapshots SNAPSHOT=^TestUISnapshot' ]
[ -z "${MEKUGI_UPDATE_UI_SNAPSHOTS-}" ]
[ -z "${GIT_INDEX_FILE-}" ]
[ "$TMPDIR" = "$(dirname "$(pwd -P)")/tmp" ]
dirname "$(pwd -P)" > "$SNAPSHOT_HOOK_TREE"
cmp rendered.txt baseline.txt
`)
	if err := os.Chmod(makePath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MEKUGI_UPDATE_UI_SNAPSHOTS", "1")
	t.Setenv("SNAPSHOT_HOOK_TREE", filepath.Join(bin, "tree"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = workspace
		return cmd.CombinedOutput()
	}
	writeTestFile(t, filepath.Join(workspace, "rendered.txt"), "old\n")
	writeTestFile(t, filepath.Join(workspace, "baseline.txt"), "old\n")
	gitTestCommit(t, workspace)
	writeTestFile(t, filepath.Join(workspace, "rendered.txt"), "new\n")
	if out, err := git("add", "rendered.txt"); err != nil {
		t.Fatalf("stage source: %v: %s", err, out)
	}
	// The working tree passes, but the staged source has a stale baseline.
	writeTestFile(t, filepath.Join(workspace, "rendered.txt"), "old\n")
	if out, err := git("-c", "core.hooksPath="+hooks, "commit", "-m", "stale"); err == nil || !strings.Contains(string(out), "Commit blocked") {
		t.Fatalf("stale staged snapshot was accepted: %v: %s", err, out)
	}
	writeTestFile(t, filepath.Join(workspace, "baseline.txt"), "new\n")
	if out, err := git("add", "baseline.txt"); err != nil {
		t.Fatalf("stage baseline: %v: %s", err, out)
	}
	// The staged tree now passes despite an unrelated unstaged source edit.
	if out, err := git("-c", "core.hooksPath="+hooks, "commit", "-m", "current"); err != nil {
		t.Fatalf("current staged snapshot was rejected: %v: %s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "rendered.txt")); err != nil || string(data) != "old\n" {
		t.Fatalf("hook changed the working tree: %q: %v", data, err)
	}
	tree, err := os.ReadFile(filepath.Join(bin, "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(tree))); !os.IsNotExist(err) {
		t.Fatalf("hook left its temporary checkout: %v", err)
	}
}

func TestDocumentationCommitHookChecksStagedDocsOnly(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	workspace := gitTestWorkspace(t)
	hooks, err := filepath.Abs("../../../../.githooks")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	makePath := filepath.Join(bin, "make")
	writeTestFile(t, makePath, `#!/bin/sh
set -eu
case "$*" in
  docs-check)
    printf 'docs\n' >> "$DOC_HOOK_LOG"
    [ ! -f guide.md ] || ! grep -q broken guide.md
    ;;
  'test-ui-snapshots SNAPSHOT_PACKAGES=@snapshots SNAPSHOT=^TestUISnapshot')
    printf 'snapshots\n' >> "$DOC_HOOK_LOG"
    ;;
  *) exit 9 ;;
esac
`)
	if err := os.Chmod(makePath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(bin, "checks")
	t.Setenv("DOC_HOOK_LOG", log)
	git := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = workspace
		return cmd.CombinedOutput()
	}
	writeTestFile(t, filepath.Join(workspace, "source.go"), "package fixture\n")
	gitTestCommit(t, workspace)
	commit := func(wantDocs, wantFailure bool) {
		t.Helper()
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := git("-c", "core.hooksPath="+hooks, "commit", "-m", "check")
		if (err != nil) != wantFailure {
			t.Fatalf("commit failure=%v, want %v: %s", err, wantFailure, out)
		}
		checks, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(checks), "docs\n") != wantDocs {
			t.Fatalf("checks=%q, want docs=%v", checks, wantDocs)
		}
	}
	stage := func(path string) {
		t.Helper()
		if out, err := git("add", path); err != nil {
			t.Fatalf("stage %s: %v: %s", path, err, out)
		}
	}
	writeTestFile(t, filepath.Join(workspace, "guide.md"), "broken\n")
	writeTestFile(t, filepath.Join(workspace, "source.go"), "package changed\n")
	stage("source.go")
	commit(false, false)
	writeTestFile(t, filepath.Join(workspace, "testdata", "example.md"), "broken\n")
	stage("testdata/example.md")
	commit(false, false)
	stage("guide.md")
	writeTestFile(t, filepath.Join(workspace, "guide.md"), "valid\n")
	commit(true, true)
	stage("guide.md")
	writeTestFile(t, filepath.Join(workspace, "guide.md"), "broken\n")
	commit(true, false)
	if out, err := git("rm", "-f", "guide.md"); err != nil {
		t.Fatalf("remove staged doc: %v: %s", err, out)
	}
	commit(true, false)
}
