package capture

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestCheckpointIntervalSelectionAndClaims(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	path, sibling := filepath.Join(workspace, "selected"), filepath.Join(workspace, "unselected")
	writeTestFile(t, path, "before\n")
	writeTestFile(t, sibling, "unselected bytes\n")
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	tree := snapshots.Checkpoint(t.Context(), workspace)
	repo := snapshots.repo(workspace)
	states, err := repo.states(t.Context(), tree, []string{sibling})
	if err != nil || states[sibling].oid == "" {
		t.Fatalf("missing fixture object: %v", err)
	}
	// An unavailable unrelated object must not be loaded by a selected read.
	oid := states[sibling].oid
	if err := os.Remove(filepath.Join(repo.gitDir, "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	observation := Observation{Tree: tree, Excluded: []string{sibling}, Programs: []execscope.Program{{Label: "git reset", VCS: true}}}
	endpoint := File{Path: path, Kind: Text, Content: "boundary\n", Size: 9}
	endpoints := []File{endpoint, endpoint, {Path: sibling}, {Path: filepath.Join(t.TempDir(), "outside")}}
	before, claims := snapshots.IntervalBefore(t.Context(), workspace, observation, observation.Files, endpoints, nil, nil)
	if len(before) != 1 || before[0].Content != "before\n" || before[0].Origin != "git reset" || before[0].Error != "" || len(claims) != 1 {
		t.Fatalf("selected checkpoint/provenance: %+v, claims=%d", before, len(claims))
	}
	writeTestFile(t, path, "unrelated live bytes\n")
	reused, _ := snapshots.IntervalBefore(t.Context(), workspace, observation, observation.Files, endpoints, claims, nil)
	if len(reused) != 1 || reused[0].Content != endpoint.Content || reused[0].Error != "" {
		t.Fatalf("claimed interval reread old or live bytes: %+v", reused)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	missing, _ := snapshots.IntervalBefore(ctx, workspace, observation, observation.Files, endpoints, nil, nil)
	if len(missing) != 1 || missing[0].Error == "" || missing[0].Content != "" {
		t.Fatalf("expired acquisition fabricated before-state: %+v", missing)
	}
	observation.Files = []File{{Path: filepath.Join(workspace, "named"), Kind: Text, Content: strings.Repeat("x", MaxContentBytes)}}
	bounded, _ := snapshots.IntervalBefore(t.Context(), workspace, observation, observation.Files, []File{endpoint}, nil, nil)
	if len(bounded) != 2 || !strings.Contains(bounded[1].Error, "budget exhausted") {
		t.Fatalf("interval exceeded aggregate inline bound: files=%d", len(bounded))
	}
	service, err := history.NewService(evidence.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []*evidence.Source{nil, {Store: store, Thread: "interval-budget"}} {
		readmitted, _ := snapshots.IntervalBefore(t.Context(), workspace, observation, observation.Files, []File{endpoint}, claims, source)
		if len(readmitted) != 2 || readmitted[1].Content != "" || source == nil && readmitted[1].Error == "" || source != nil && readmitted[1].ContentRef == nil {
			t.Fatalf("claimed endpoint bypassed remaining inline budget: source=%v files=%d", source != nil, len(readmitted))
		}
		if source != nil {
			if text, err := readmitted[1].Text(); err != nil || text != endpoint.Content {
				t.Fatalf("spilled claim lost bytes: %q, %v", text, err)
			}
		}
	}
	observation.Files = nil
	unknown, claimed := snapshots.IntervalBefore(t.Context(), workspace, observation, nil, []File{{Path: filepath.Join(workspace, "unadmitted"), Kind: Text, Content: "boundary\n"}}, nil, nil)
	if len(unknown) != 1 || unknown[0].Error == "" || len(claimed) != 0 {
		t.Fatalf("bounded checkpoint absence fabricated a creation: %+v", unknown)
	}
}

func TestCheckpointIntervalsShareParentIdentity(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			isolateSnapshotGit(t)
			physical := t.TempDir()
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(physical, alias); err != nil {
				t.Fatal(err)
			}
			workspace, endpointRoot := alias, physical
			if reverse {
				workspace, endpointRoot = physical, alias
			}
			writeTestFile(t, filepath.Join(physical, "file"), "A\n")
			writeTestFile(t, filepath.Join(physical, "excluded"), "excluded\n")
			writeTestFile(t, filepath.Join(physical, "nested", "file"), "nested\n")
			if err := os.Symlink("file", filepath.Join(physical, "link")); err != nil {
				t.Fatal(err)
			}
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			tree := snapshots.Checkpoint(t.Context(), workspace)
			if tree == "" {
				t.Fatal("missing checkpoint")
			}
			// A newly witnessed nested repository excludes its old tree entries.
			writeTestFile(t, filepath.Join(physical, "nested", ".git"), "gitdir: elsewhere\n")
			observation := Observation{Tree: tree, Excluded: []string{filepath.Join(workspace, "excluded")}}
			endpoints := []File{
				{Path: filepath.Join(endpointRoot, "file"), Kind: Text, Content: "B\n"},
				{Path: filepath.Join(endpointRoot, "link"), Kind: Symlink, Link: "new-target"},
				{Path: filepath.Join(endpointRoot, "excluded"), Kind: Text},
				{Path: filepath.Join(endpointRoot, "nested", "file"), Kind: Text},
			}
			before, claims := snapshots.IntervalBefore(t.Context(), workspace, observation, nil, endpoints, nil, nil)
			if len(before) != 2 || before[0].Content != "A\n" || before[0].Error != "" || before[1].Kind != Symlink || before[1].Link != "file" || before[1].Error != "" {
				t.Fatalf("alias membership changed scope or leaf identity: %+v", before)
			}
			claims.Forget([]File{{Path: filepath.Join(workspace, "file")}})
			if len(claims) != 1 || claims[filepath.Join(physical, "link")].file == nil {
				t.Fatalf("claim retirement lost physical membership: %+v", claims)
			}
		})
	}
}

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
