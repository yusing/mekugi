package presentation

import (
	"reflect"
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/activitydetail"
)

func TestSavedDiffTemporaryPathsPreserveEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, workspace, selected, before, after string
		hidden                                   bool
	}{
		{"create", "/w", "/w", "", "/tmp/new", true},
		{"delete", "/w", "/w", "/tmp/old", "", true},
		{"move within", "/w", "/w", "/tmp/old", "/tmp/new", true},
		{"root", "/w", "/w", "/tmp", "/tmp", true},
		{"clean path", "/w", "/w", "", "/w/../tmp/new", true},
		{"relative capture", "/tmp/work", "/project", "old", "new", false},
		{"relative selected", "", "/tmp/work", "old", "new", false},
		{"capture wins", "/project", "/tmp/work", "old", "new", false},
		{"absolute workspace capture", "/tmp/work", "/project", "", "/tmp/work/file.go", false},
		{"external temporary sibling", "/tmp/work", "/tmp/work", "", "/tmp/work-other/file.go", true},
		{"unknown workspace", "", "", "old", "new", false},
		{"sibling prefix", "/w", "/w", "", "/tmpfoo/new", false},
		{"project tmp", "/w", "/w", "", "tmp/new", false},
		{"other temporary", "/w", "/w", "", "/var/tmp/new", false},
		{"move out", "/w", "/w", "/tmp/old", "/w/new", false},
		{"move in", "/w", "/w", "/w/old", "/tmp/new", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			chunk := netCountsCapture(1, "/root", tt.before, tt.after, "old\n", "new\n")
			chunk.Workspace = tt.workspace
			attempt := snapshotAttempt("capture", "thread", "item", chunk)
			snapshot := snapshotFixture(t, attempt)
			request := activitydetail.Request{Operation: attempt.Operation}
			detail := snapshot.ActivityDetail(request)
			counts := snapshot.Publication.Counts
			pane := snapshot.Publication.ForDiff(tt.selected)
			if (len(pane.Files) == 0) != tt.hidden {
				t.Fatalf("saved pane files: %+v, hidden=%t", pane.Files, tt.hidden)
			}
			if !tt.hidden && !reflect.DeepEqual(pane.Files[0].Chunks, []Chunk{chunk}) {
				t.Fatal("visible capture lost its original endpoints or content")
			}
			if !reflect.DeepEqual(slices.Collect(snapshot.Attempts()), []Attempt{attempt}) ||
				!reflect.DeepEqual(snapshot.ActivityDetail(request), detail) || len(detail.Pages) != 1 ||
				!reflect.DeepEqual(snapshot.Publication.Counts, counts) || len(snapshot.Publication.Files) != 1 {
				t.Fatal("saved pane filtering changed retained evidence, detail or totals")
			}
		})
	}
}
