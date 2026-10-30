package capture

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"
)

func TestCapturePreviewAndNeutralCommandsSkipCheckpoints(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "neutral", true: "preview"}[preview], func(t *testing.T) {
			workspace := t.TempDir()
			command := "cat source.txt"
			if preview {
				command = "printf after > source.txt"
			}
			writeTestFile(t, filepath.Join(workspace, "source.txt"), "before\n")
			snapshots := newWorkspaces(t.Context(), t.TempDir())
			t.Cleanup(snapshots.Close)
			observation, observed := Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false,
				Options{Directory: workspace, Workspaces: snapshots, PreviewOnly: preview})
			if observed != preview || len(snapshots.repos) != 0 || observation != nil && observation.Tree != "" {
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
