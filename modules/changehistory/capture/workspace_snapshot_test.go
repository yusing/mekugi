package capture

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis"
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

func TestCheckpointIntervalAbsentBoundary(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	existing, created := filepath.Join(workspace, "existing"), filepath.Join(workspace, "created")
	writeTestFile(t, existing, "before\n")
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	observation, _ := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: workspace, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
	tree := observation.Tree
	if tree == "" {
		t.Fatal("missing checkpoint")
	}
	for _, scenario := range []struct {
		name       string
		historical bool
		tree       string
		endpoint   File
		claims     Claims
		wantText   string
		wantGap    bool
	}{
		{name: "new-file", tree: tree, endpoint: File{Path: created}},
		{name: "real-deletion", tree: tree, endpoint: File{Path: existing}, wantText: "before\n"},
		{name: "historical-absent-boundary", historical: true, tree: tree, endpoint: File{Path: created}, wantGap: true},
		{name: "unavailable-before", historical: true, tree: tree, endpoint: File{Path: created, Kind: Text, Content: "unknown origin\n"}, wantGap: true},
		{name: "unreadable-boundary", tree: tree, endpoint: File{Path: created, Error: "unreadable"}, wantGap: true},
		{name: "missing-checkpoint", endpoint: File{Path: created}, wantGap: true},
		{name: "claimed-deletion", tree: tree, endpoint: File{Path: created}, claims: Claims{created: {repo: snapshots.repo(workspace), file: &File{Path: created, Kind: Text, Content: "claimed\n"}}}, wantText: "claimed\n"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			endpoints := []File{scenario.endpoint}
			observed := *observation
			observed.Tree = scenario.tree
			if scenario.historical {
				observed.CheckpointOmissions = nil
			}
			before, claims := snapshots.IntervalBefore(t.Context(), workspace, observed, nil, endpoints, scenario.claims, nil)
			if len(before) != 1 || before[0].Content != scenario.wantText || (before[0].Error != "") != (scenario.wantGap && scenario.endpoint.Error == "") {
				t.Fatalf("before=%+v", before)
			}
			reviews, boundaries := ReviewInterval(before, endpoints)
			if len(boundaries) != 1 {
				t.Fatal("lost continuation boundary")
			}
			if scenario.wantGap {
				if len(claims) != 0 || len(reviews) != 1 || reviews[0].Incomplete == "" {
					t.Fatalf("unknown coverage accepted: claims=%d reviews=%+v", len(claims), reviews)
				}
			} else if scenario.wantText != "" {
				if len(claims) != 1 || len(reviews) != 1 || reviews[0].BeforePath != scenario.endpoint.Path || reviews[0].AfterPath != "" || reviews[0].Incomplete != "" {
					t.Fatalf("lost witnessed deletion: %+v", reviews)
				}
			} else if len(claims) != 1 || len(reviews) != 0 {
				t.Fatalf("invented pre-creation effect: %+v", reviews)
			}
		})
	}
}

func TestCheckpointIntervalUnretainedDeletionBeforeCreation(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "seed"), "seed\n")
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	if snapshots.Checkpoint(t.Context(), workspace) == "" {
		t.Fatal("missing initial checkpoint")
	}
	for i := range workspaceSnapshotPlainAdds + 1 {
		writeTestFile(t, filepath.Join(workspace, "burst", fmt.Sprint(i)), "unretained before\n")
	}
	path := filepath.Join(workspace, "burst", "0")
	observation, _ := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: workspace, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
	tree := observation.Tree
	if !slices.Equal(observation.CheckpointOmissions[workspace], []string{filepath.Join(workspace, "burst")}) {
		t.Fatalf("unadmitted directory was not collapsed: %v", observation.CheckpointOmissions)
	}
	states, err := snapshots.repo(workspace).states(t.Context(), tree, []string{path})
	if err != nil || tree == "" || len(states) != 0 {
		t.Fatalf("fixture did not omit the existing file: tree=%q states=%v err=%v", tree, states, err)
	}
	// The open writer deletes an existing, unretained file. A later creation
	// witnesses absence, but that cannot establish the writer's before-state.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	endpoints := []File{{Path: path}}
	before, claims := snapshots.IntervalBefore(t.Context(), workspace, *observation, nil, endpoints, nil, nil)
	reviews, _ := ReviewInterval(before, endpoints)
	if len(before) != 1 || before[0].Error == "" || len(claims) != 0 || len(reviews) != 1 || reviews[0].Incomplete == "" {
		t.Fatalf("unavailable before-state became complete: before=%+v claims=%d reviews=%+v", before, len(claims), reviews)
	}
}

func TestCheckpointAbsenceInventory(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%v", git), func(t *testing.T) {
			isolateSnapshotGit(t)
			workspace := t.TempDir()
			if git {
				command := exec.Command("git", "-C", workspace, "init", "--quiet")
				command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("git init: %s %v", output, err)
				}
			}
			writeTestFile(t, filepath.Join(workspace, "indexed", "file"), "before\n")
			writeTestFile(t, filepath.Join(workspace, ".gitignore"), "ignored/\n")
			writeTestFile(t, filepath.Join(workspace, "ignored", "file"), "ignored\n")
			if err := os.Mkdir(filepath.Join(workspace, "empty"), 0700); err != nil {
				t.Fatal(err)
			}
			// Git does not list special files as untracked; they still existed.
			pipe := filepath.Join(workspace, "pipe")
			if output, err := exec.Command("mkfifo", pipe).CombinedOutput(); err != nil {
				t.Fatalf("mkfifo: %s %v", output, err)
			}
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			observation, _ := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: workspace, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
			omitted := observation.CheckpointOmissions[workspace]
			// Collapse ignored directories, never enumerate their contents.
			if !slices.Contains(omitted, filepath.Join(workspace, "ignored")) || !slices.Contains(omitted, pipe) || !slices.Contains(omitted, filepath.Join(workspace, "empty")) {
				t.Fatalf("inventory lost omitted entry roots: %v", omitted)
			}
			if err := os.Remove(pipe); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(workspace, "empty")); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"empty", "pipe", "indexed/new", "new-directory/new"} {
				path := filepath.Join(workspace, name)
				before, _ := snapshots.IntervalBefore(t.Context(), workspace, *observation, nil, []File{{Path: path}}, nil, nil)
				gap := name == "empty" || name == "pipe"
				if len(before) != 1 || (before[0].Error != "") != gap {
					t.Fatalf("%s: before=%+v", name, before)
				}
			}
			if os.Geteuid() != 0 {
				directory := filepath.Join(workspace, "indexed")
				if err := os.Chmod(directory, 0); err != nil {
					t.Fatal(err)
				}
				omitted := snapshots.repo(workspace).omittedPaths(t.Context())
				if err := os.Chmod(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if omitted != nil {
					t.Fatalf("unreadable directory became absence: %v", omitted)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if got := snapshots.repo(workspace).omittedPaths(ctx); got != nil {
				t.Fatalf("canceled inventory became absence: %v", got)
			}
		})
	}
}

func TestCheckpointAbsenceInventoryKeepsEntryBound(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	seed := filepath.Join(workspace, "seed")
	writeTestFile(t, seed, "seed\n")
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	tree := snapshots.Checkpoint(t.Context(), workspace)
	repo := snapshots.repo(workspace)
	states, err := repo.states(t.Context(), tree, []string{seed})
	if err != nil || states[seed].oid == "" {
		t.Fatalf("missing fixture tree: %v", err)
	}
	// Populate only the private index, avoiding thousands of fixture files.
	var entries strings.Builder
	for i := range workspaceSnapshotPlainFiles {
		fmt.Fprintf(&entries, "100644 %s\tfile-%d\n", states[seed].oid, i)
	}
	if _, err := repo.run(t.Context(), strings.NewReader(entries.String()), "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if omitted := repo.omittedPaths(t.Context()); omitted != nil {
		t.Fatalf("oversized inventory established absence: %v", omitted)
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
