package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSnapshotRecoversFromPrunedState(t *testing.T) {
	isolateSnapshotGit(t)
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "a.txt"), "a\n")
	if snapshots.Checkpoint(t.Context(), workspace) == "" {
		t.Fatal("no first checkpoint")
	}
	// Another router process pruned this workspace's state as idle.
	if err := os.RemoveAll(filepath.Dir(snapshots.repo(workspace).lockPath)); err != nil {
		t.Fatal(err)
	}
	if snapshots.Checkpoint(t.Context(), workspace) == "" {
		t.Fatal("pruned state was not rebuilt")
	}
}

func TestWorkspaceSnapshotRetriesAFailedWarmup(t *testing.T) {
	isolateSnapshotGit(t)
	store := t.TempDir()
	blocker := filepath.Join(store, workspaceSnapshotDirectory)
	writeTestFile(t, blocker, "not a directory\n")
	snapshots := newWorkspaces(t.Context(), store)
	t.Cleanup(snapshots.Close)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "a.txt"), "a\n")
	if snapshots.Checkpoint(t.Context(), workspace) != "" {
		t.Fatal("checkpoint despite an unwritable store")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	repo := snapshots.repo(workspace)
	repo.mu.Lock()
	repo.failed = time.Now().Add(-workspaceSnapshotRetry)
	repo.mu.Unlock()
	snapshots.Checkpoint(t.Context(), workspace) // Starts the retry.
	repo.mu.Lock()
	ready := repo.ready
	repo.mu.Unlock()
	<-ready
	if snapshots.Checkpoint(t.Context(), workspace) == "" {
		t.Fatal("a failed warmup was never retried")
	}
}

// A refreshed private index may drop an ignored entry retained by an older
// tree. This is not an on-disk deletion and cannot become an incomplete edit.
func TestWorkspaceSnapshotIgnoredIndexEntryIsNotDeletion(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", workspace}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v\n%s", err, output)
		}
	}
	runGit("init", "--quiet")
	tracked := filepath.Join(workspace, "tracked.log")
	writeTestFile(t, tracked, "before\n")
	runGit("add", "tracked.log")
	writeTestFile(t, filepath.Join(workspace, "build/cache.bin"), strings.Repeat("\x00", MaxFileBytes+1))
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	tree := snapshots.Checkpoint(t.Context(), workspace)
	if tree == "" {
		t.Fatal("missing baseline")
	}
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "/build/\n*.log\n")
	writeTestFile(t, tracked, "after\n")
	if _, err := snapshots.repo(workspace).run(t.Context(), nil, "update-index", "--force-remove", "--", "build/cache.bin"); err != nil {
		t.Fatal(err)
	}
	result := Reconcile(t.Context(), Observation{Tree: tree}, Comparison{Workspaces: snapshots, Workspace: workspace})
	if result.SnapshotError != nil {
		t.Fatal(result.SnapshotError)
	}
	reviews, complete, coverage := result.Files, result.Complete, result.Coverage
	if !complete || coverage != CoverageExact || len(reviews) != 2 {
		t.Fatalf("incomplete ignore transition: %+v complete=%v coverage=%s", reviews, complete, coverage)
	}
	for _, review := range reviews {
		if strings.HasSuffix(review.BeforePath, "cache.bin") || strings.HasSuffix(review.AfterPath, "cache.bin") {
			t.Fatalf("ignored artifact became a change: %+v", review)
		}
	}
}
