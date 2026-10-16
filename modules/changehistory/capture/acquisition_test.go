package capture

import (
	"path/filepath"
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
			snapshots := NewWorkspaces(t.TempDir())
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
	snapshots := NewWorkspaces(t.TempDir())
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
}
