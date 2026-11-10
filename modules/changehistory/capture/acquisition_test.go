package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/libraries/commandanalysis"
)

func TestCapturePreviewAndNeutralCommandsSkipCheckpoints(t *testing.T) {
	for _, scenario := range []struct {
		name, command string
		preview       bool
	}{
		{"neutral", "cat source.txt", false},
		{"bounded-neutral", "mrun --max-tokens 100 cat source.txt", false},
		{"preview", "printf after > source.txt", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "source.txt"), "before\n")
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			observation, observed := Capture(t.Context(), []commandanalysis.Command{{Command: scenario.command, Workdir: workspace, Shell: "bash"}}, false, false,
				Options{Directory: workspace, Workspaces: snapshots, PreviewOnly: scenario.preview})
			if observed != scenario.preview || len(snapshots.repos) != 0 || observation != nil && observation.Tree != "" {
				t.Fatalf("unexpected checkpoint: observed=%v observation=%+v repositories=%d", observed, observation, len(snapshots.repos))
			}
		})
	}
}

func TestCaptureWorkspaceFallbackAndUnavailableComparison(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	writeTestFile(t, path, "before\n")
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	observation, observed := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: workspace, Shell: "bash"}}, false, false,
		Options{Directory: workspace, Workspaces: snapshots})
	if !observed || observation == nil || observation.Tree == "" {
		t.Fatalf("missing opaque baseline: observed=%v observation=%+v", observed, observation)
	}
	writeTestFile(t, path, "after\n")
	result := Reconcile(t.Context(), *observation, Comparison{Workspace: workspace, Workspaces: snapshots})
	if result.SnapshotError != nil || !result.Complete || result.Coverage != CoverageExact || len(result.Files) != 1 || len(result.Claims) == 0 {
		t.Fatalf("opaque comparison: %+v", result)
	}
	if !strings.Contains(result.Files[0].Diff, "+after") {
		t.Fatalf("missing observed effect: %+v", result.Files)
	}

	// An unavailable workspace comparison keeps independently captured named
	// evidence and reports partial coverage rather than a successful full sweep.
	observation.Files = []File{ReadFile(path, nil)}
	observation.Tree = "missing-checkpoint"
	writeTestFile(t, path, "latest\n")
	result = Reconcile(t.Context(), *observation, Comparison{Workspace: workspace, Workspaces: snapshots})
	if result.SnapshotError == nil || !result.Complete || result.Coverage != CoveragePartial || len(result.Files) != 1 || !strings.Contains(result.Files[0].Diff, "+latest") {
		t.Fatalf("unavailable fallback discarded named evidence: %+v", result)
	}
	if !strings.Contains(result.ScopeReason, "workspace snapshot unavailable: "+result.SnapshotError.Error()) {
		t.Fatalf("missing retained coverage diagnostic: %+v", result.Outcome)
	}
}

func TestReconcileAssessesCapturedOutcome(t *testing.T) {
	workspace := t.TempDir()
	directory := filepath.Join(workspace, "removed")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "source.txt")
	writeTestFile(t, path, "before\n")
	observation, observed := Capture(t.Context(), []commandanalysis.Command{{Command: "rm -r removed", Workdir: workspace, Shell: "bash"}}, false, true, Options{Directory: workspace})
	if !observed {
		t.Fatal("missing directory capture")
	}
	for _, target := range []string{path, directory} {
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
	}
	observation.RepeatedPaths = true
	result := Reconcile(t.Context(), *observation, Comparison{})
	if !result.Complete || result.Coverage != CoverageExact || len(result.Files) != 1 || result.Files[0].BeforePath != path || result.Files[0].AfterPath != "" {
		t.Fatalf("directory effect = %+v", result)
	}
	if !slices.Equal(result.DeletedDirectories, []string{directory}) || !slices.Equal(result.Scope, observation.ScopePaths()) || result.Class != observation.Class || !slices.Equal(result.Labels, observation.Labels) || !result.CodeMode {
		t.Fatalf("capture assessment = %+v", result.Outcome)
	}
	if !strings.Contains(result.ScopeReason, "intermediate writes unobserved (call-window comparison only)") || result.Status != "" || result.Exit != nil || result.OutputRef != "" {
		t.Fatalf("capture assessment changed host or publication facts: %+v", result.Outcome)
	}
}

func TestReconcileRepeatedWritesKeepPriorChangedIntervals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	writeTestFile(t, path, "unchanged suffix\n")
	observation := Observation{Files: []File{ReadFile(path, nil)}, RepeatedPaths: true}
	for _, prior := range []bool{false, true} {
		result := Reconcile(t.Context(), observation, Comparison{PriorChanges: prior})
		if result.Complete != prior || (result.Coverage == CoverageExact) != prior || len(result.Files) != 0 || !strings.Contains(result.ScopeReason, "intermediate writes unobserved") {
			t.Fatalf("prior changes=%v: %+v", prior, result)
		}
	}
	observation.Files[0].Error = "missing boundary"
	result := Reconcile(t.Context(), observation, Comparison{PriorChanges: true})
	if result.Complete || result.Coverage != CoveragePartial || len(result.Files) != 1 || result.Files[0].Incomplete != "missing boundary" {
		t.Fatalf("prior changes hid unavailable evidence: %+v", result)
	}
}

func TestCaptureExecutionDirectoriesReuseCoveredScope(t *testing.T) {
	workspace := t.TempDir()
	child := filepath.Join(workspace, "ordinary", "subdir")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(workspace, ".worktrees", "other", "src")
	writeTestFile(t, filepath.Join(filepath.Dir(nested), ".git"), "gitdir: elsewhere\n")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	commands := []commandanalysis.Command{
		{Workdir: child}, {Workdir: nested}, {Workdir: nested}, {Workdir: outside}, {Workdir: "/read-only"},
	}
	got := checkpointDirectories(workspace, commands, []string{"opaque", "opaque", "opaque", "opaque", "neutral"})
	if !slices.Equal(got, []string{workspace, nested, outside}) {
		t.Fatalf("checkpoint scope duplicated a covered directory or omitted a repository: %q", got)
	}
}

func TestMergedCommandDirectoryCheckpointsKeepEarliestAdmission(t *testing.T) {
	first := Observation{Checkpoints: map[string]string{"/external": "first"}, CheckpointOmissions: map[string][]string{"/external": {"/external/omitted"}}}
	second := Observation{Checkpoints: map[string]string{"/external": "second", "/other": "other"}, CheckpointOmissions: map[string][]string{"/external": {}, "/other": {}}}
	merged := Merge([]Member{{Observation: second, Sequence: 20}, {Observation: first, Sequence: 10}})
	if merged.Checkpoints["/external"] != "first" || merged.Checkpoints["/other"] != "other" {
		t.Fatalf("merged checkpoints lost admission order: %+v", merged.Checkpoints)
	}
	if !slices.Equal(merged.CheckpointOmissions["/external"], first.CheckpointOmissions["/external"]) || merged.CheckpointOmissions["/other"] == nil {
		t.Fatalf("inventory lost checkpoint admission order: %+v", merged.CheckpointOmissions)
	}
	merged.CheckpointOmissions["/external"] = nil
	if first.CheckpointOmissions["/external"] == nil {
		t.Fatal("merge mutated original inventory")
	}
	first.CheckpointOmissions = nil
	merged = Merge([]Member{{Observation: second, Sequence: 20}, {Observation: first, Sequence: 10}})
	if _, found := merged.CheckpointOmissions["/external"]; found {
		t.Fatal("later inventory supplied earlier missing coverage")
	}
	merged.Checkpoints["/external"] = "mutated"
	if first.Checkpoints["/external"] != "first" || second.Checkpoints["/external"] != "second" {
		t.Fatal("merge mutated its input checkpoints")
	}
}

// These scope fixtures exercise real Git and cross-store evidence, not forwarding
// latency. Keep subprocess scheduling outside the fake clock so host contention
// cannot consume the shared acquisition/reconciliation deadline. Owners and their
// cleanup stay inside each bubble; production timeout tests retain real time.
func TestOverlappingDirectoryClaimsSharePhysicalEvidence(t *testing.T) {
	isolateSnapshotGit(t)
	for _, useAlias := range []bool{false, true} {
		t.Run(map[bool]string{false: "sub", true: "linked"}[useAlias], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				workspace, external := t.TempDir(), t.TempDir()
				sub := filepath.Join(external, "sub")
				path := filepath.Join(sub, "source.txt")
				writeTestFile(t, path, "before\n")
				alias := filepath.Join(workspace, "linked")
				if err := os.Symlink(sub, alias); err != nil {
					t.Fatal(err)
				}
				// Different runtime owners have independent private object stores.
				first, second := newWorkspaces(t.Context(), t.TempDir()), newWorkspaces(t.Context(), t.TempDir())
				t.Cleanup(first.Close)
				t.Cleanup(second.Close)
				capture := func(directory string, snapshots *Workspaces) Observation {
					t.Helper()
					observation, ok := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: directory, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
					if !ok || observation == nil || observation.Checkpoints[directory] == "" || !slices.Contains(observation.Roots, sub) && directory == alias {
						t.Fatalf("missing external physical scope: %+v", observation)
					}
					return *observation
				}
				directory := sub
				if useAlias {
					directory = alias
				}
				writeTestFile(t, path, "before\n")
				a, b := capture(external, first), capture(directory, second)
				writeTestFile(t, path, "after\n")
				initial := Reconcile(t.Context(), a, Comparison{Workspace: workspace, Workspaces: first})
				if initial.SnapshotError != nil || len(initial.Files) != 1 || initial.Coverage != CoverageExact {
					t.Fatalf("first writer: %+v", initial)
				}
				duplicate := Reconcile(t.Context(), b, Comparison{Workspace: workspace, Workspaces: second, Claims: initial.Claims})
				if duplicate.SnapshotError != nil || len(duplicate.Files) != 0 || duplicate.Coverage != CoverageExact {
					t.Fatalf("overlapping scope duplicated claimed effects: %+v", duplicate)
				}
				// The original baseline equals disk again. Claims must still supply
				// the previous endpoint, including bytes from the other private store.
				writeTestFile(t, path, "before\n")
				restored := Reconcile(t.Context(), b, Comparison{Workspace: workspace, Workspaces: second, Claims: initial.Claims})
				if restored.SnapshotError != nil || len(restored.Files) != 1 || !strings.Contains(restored.Files[0].Diff, "-after\n+before\n") {
					t.Fatalf("claimed cross-store restore missing: %+v", restored)
				}
				writeTestFile(t, path, "latest\n")
				latest := Reconcile(t.Context(), b, Comparison{Workspace: workspace, Workspaces: second, Claims: initial.Claims})
				if latest.SnapshotError != nil || len(latest.Files) != 1 || !strings.Contains(latest.Files[0].Diff, "-after\n+latest\n") {
					t.Fatalf("claimed cross-store update missing: %+v", latest)
				}
			})
		})
	}
}

func TestSymlinkDirectoryCheckpointPreservesGitScope(t *testing.T) {
	isolateSnapshotGit(t)
	workspace := t.TempDir()
	repository := filepath.Join(workspace, "nested")
	sub := filepath.Join(repository, "sub")
	writeTestFile(t, filepath.Join(sub, "source.txt"), "before\n")
	writeTestFile(t, filepath.Join(repository, ".gitignore"), "ignored.txt\n")
	writeTestFile(t, filepath.Join(sub, "ignored.txt"), "ignored before\n")
	for _, args := range [][]string{{"init", "--quiet"}, {"add", ".gitignore", "sub/source.txt"}} {
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Git fixture: %s: %v", output, err)
		}
	}
	alias := filepath.Join(workspace, "linked")
	if err := os.Symlink(sub, alias); err != nil {
		t.Fatal(err)
	}
	snapshots := newWorkspaces(t.Context(), t.TempDir())
	t.Cleanup(snapshots.Close)
	observation, ok := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: alias, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
	if !ok || observation == nil || observation.Checkpoints[alias] == "" {
		t.Fatalf("symlink hid independent nested Git authority: %+v", observation)
	}
	writeTestFile(t, filepath.Join(sub, "source.txt"), "after\n")
	writeTestFile(t, filepath.Join(sub, "ignored.txt"), "ignored after\n")
	result := Reconcile(t.Context(), *observation, Comparison{Workspace: workspace, Workspaces: snapshots})
	if result.SnapshotError != nil || len(result.Files) != 1 || result.Coverage != CoverageExact || result.Files[0].AfterPath != filepath.Join(alias, "source.txt") {
		t.Fatalf("symlink scope lost Git ignores or lexical evidence paths: %+v", result)
	}
}

func TestNestedGitClaimsDoNotInventEnclosingDeletions(t *testing.T) {
	isolateSnapshotGit(t)
	for _, gitParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "git"}[gitParent], func(t *testing.T) {
			workspace, parent := t.TempDir(), t.TempDir()
			nested := filepath.Join(parent, "nested")
			path, sibling := filepath.Join(nested, "source.txt"), filepath.Join(parent, "sibling.txt")
			writeTestFile(t, path, "before\n")
			writeTestFile(t, sibling, "before\n")
			git := func(directory string, args ...string) {
				t.Helper()
				command := exec.Command("git", append([]string{"-C", directory, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
				command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("Git fixture: %s: %v", output, err)
				}
			}
			if gitParent {
				git(parent, "init", "--quiet")
			}
			git(nested, "init", "--quiet")
			git(nested, "add", "source.txt")
			git(nested, "commit", "--quiet", "-m", "fixture")
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			capture := func(directory string) Observation {
				t.Helper()
				observation, ok := Capture(t.Context(), []commandanalysis.Command{{Command: "custom-generator", Workdir: directory, Shell: "bash"}}, false, false, Options{Directory: workspace, Workspaces: snapshots})
				if !ok || observation == nil || observation.Checkpoints[directory] == "" {
					t.Fatalf("missing nested-scope baseline: %+v", observation)
				}
				return *observation
			}
			a, b := capture(nested), capture(parent)
			writeTestFile(t, path, "after\n")
			writeTestFile(t, sibling, "after\n")
			first := Reconcile(t.Context(), a, Comparison{Workspace: workspace, Workspaces: snapshots})
			if first.SnapshotError != nil || len(first.Files) != 1 {
				t.Fatalf("nested writer: %+v", first)
			}
			result := Reconcile(t.Context(), b, Comparison{Workspace: workspace, Workspaces: snapshots, Claims: first.Claims})
			if result.SnapshotError != nil || len(result.Files) != 1 || result.Files[0].AfterPath != sibling || result.Coverage != CoverageExact {
				t.Fatalf("nested authority claim fabricated enclosing effects: %+v", result)
			}
		})
	}
}

func TestSnapshotAliasesRespectNamedEvidenceAndExclusions(t *testing.T) {
	isolateSnapshotGit(t)
	synctest.Test(t, func(t *testing.T) {
		base := t.TempDir()
		workspace, external, alias := filepath.Join(base, "workspace"), filepath.Join(base, "z-external"), filepath.Join(base, "a-alias")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(external, "sub", "source.txt")
		writeTestFile(t, path, "before\n")
		if err := os.Symlink(external, alias); err != nil {
			t.Fatal(err)
		}
		snapshots := newWorkspaces(t.Context(), t.TempDir())
		t.Cleanup(snapshots.Close)
		observation, ok := Capture(t.Context(), []commandanalysis.Command{
			{Command: "printf after > sub/source.txt", Workdir: external, Shell: "bash"},
			{Command: "custom-generator", Workdir: alias, Shell: "bash"},
		}, false, false, Options{Directory: workspace, Workspaces: snapshots})
		if !ok || observation == nil || len(observation.Files) != 1 || len(observation.Checkpoints) != 2 {
			t.Fatalf("missing named and alias checkpoint scopes: %+v", observation)
		}
		writeTestFile(t, path, "after\n")
		result := Reconcile(t.Context(), *observation, Comparison{Workspace: workspace, Workspaces: snapshots})
		if result.SnapshotError != nil || len(result.Files) != 1 || result.Files[0].AfterPath != path || result.Coverage != CoverageExact {
			t.Fatalf("alias duplicated named evidence or replaced its lexical path: %+v", result)
		}
		for _, root := range []string{path, filepath.Join(alias, "sub", "source.txt"), external, alias} {
			// Overlapping windows exclude only unnamed fallback effects; same-call
			// patch ownership excludes named evidence as well.
			unnamed := *observation
			unnamed.Files = nil
			result := Reconcile(t.Context(), unnamed, Comparison{Workspace: workspace, Workspaces: snapshots, Excluded: []string{root}})
			if result.SnapshotError != nil || len(result.Files) != 0 {
				t.Fatalf("alias escaped overlapping scope %s: %+v", root, result)
			}
			patched := *observation
			patched.Excluded = []string{root}
			result = Reconcile(t.Context(), patched, Comparison{Workspace: workspace, Workspaces: snapshots})
			if result.SnapshotError != nil || len(result.Files) != 0 {
				t.Fatalf("alias escaped same-call patch scope %s: %+v", root, result)
			}
		}
		// Removed parents retain alias identity through their existing ancestor.
		for _, target := range []string{path, filepath.Dir(path)} {
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
		}
		result = Reconcile(t.Context(), *observation, Comparison{Workspace: workspace, Workspaces: snapshots})
		if result.SnapshotError != nil || len(result.Files) != 1 || result.Files[0].BeforePath != path || result.Files[0].AfterPath != "" {
			t.Fatalf("removed parent directory broke physical deduplication: %+v", result)
		}
	})
}
