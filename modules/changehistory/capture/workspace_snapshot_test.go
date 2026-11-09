package capture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestWorkspaceSnapshotSymlinkTargetsIgnoreTextBudgetAndClassification(t *testing.T) {
	for _, target := range []string{"old-target", "old-\xff"} {
		t.Run(fmt.Sprintf("utf8=%v", utf8.ValidString(target)), func(t *testing.T) {
			isolateSnapshotGit(t)
			workspace := t.TempDir()
			path, regular := filepath.Join(workspace, "link"), filepath.Join(workspace, "regular")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, regular, target) // The same Git object can have both modes.
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			tree := snapshots.Checkpoint(t.Context(), workspace)
			if tree == "" {
				t.Fatal("missing checkpoint")
			}
			repo := snapshots.repo(workspace)
			states, err := repo.states(t.Context(), tree, []string{path, regular})
			if err != nil {
				t.Fatal(err)
			}
			service, err := history.NewService(evidence.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := service.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := &evidence.Source{Store: store, Thread: "links"}
			budget := 0
			blobs, err := repo.blobs(t.Context(), []workspaceSnapshotEntry{states[path], states[regular]}, &budget, source)
			if err != nil {
				t.Fatal(err)
			}
			before := workspaceSnapshotFile(path, states[path], blobs)
			if before.Kind != Symlink || before.Link != target || before.Error != "" {
				t.Fatalf("lost target after spilling/classification: %+v", before)
			}
			if utf8.ValidString(target) && workspaceSnapshotFile(regular, states[regular], blobs).ContentRef == nil {
				t.Fatal("regular side did not exercise exhausted inline budget")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("new-target", path); err != nil {
				t.Fatal(err)
			}
			result := Reconcile(t.Context(), Observation{Files: []File{before}}, Comparison{Evidence: source})
			if !result.Complete || len(result.Files) != 1 {
				t.Fatalf("changed link disappeared: complete=%v files=%d", result.Complete, len(result.Files))
			}
			file, err := result.Files[0].Load()
			if err != nil || !file.Link || !strings.Contains(file.Diff, "--> "+target) || !strings.Contains(file.Diff, "+-> new-target") {
				t.Fatalf("link review lost target: %+v, %v", file, err)
			}
		})
	}
}

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
