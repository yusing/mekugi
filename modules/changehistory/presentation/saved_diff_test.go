package presentation

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
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

func TestSavedDiffEmptyNetPreservesHistory(t *testing.T) {
	for _, kind := range []string{"rewrite", "create-delete", "delete-recreate", "move-back", "binary", "incomplete", "inconsistent", "move", "empty-create", "empty-delete"} {
		t.Run(kind, func(t *testing.T) {
			first := netCountsCapture(1, "/root", "/w/a", "/w/a", "old\n", "draft\n")
			second := netCountsCapture(2, "/root/worker", "/w/a", "/w/a", "draft\n", "old\n")
			hidden := true
			switch kind {
			case "create-delete":
				first.Review = diffmodel.RenderReviewFile("", "/w/a", "", "draft\n")
				second.Review = diffmodel.RenderReviewFile("/w/a", "", "draft\n", "")
			case "delete-recreate":
				first.Review = diffmodel.RenderReviewFile("/w/a", "", "old\n", "")
				second.Review = diffmodel.RenderReviewFile("", "/w/a", "", "old\n")
			case "move-back":
				first.Review = diffmodel.RenderReviewFile("/w/a", "/w/b", "old\n", "old\n")
				second.Review = diffmodel.RenderReviewFile("/w/b", "/w/a", "old\n", "old\n")
			case "binary":
				first.Review.Binary = true
				hidden = false
			case "incomplete":
				first.Review.Incomplete = "missing baseline"
				hidden = false
			case "inconsistent":
				second.Review = diffmodel.RenderReviewFile("/w/a", "/w/a", "unobserved\n", "old\n")
				hidden = false
			case "move":
				second.Review = diffmodel.RenderReviewFile("/w/a", "/w/b", "draft\n", "old\n")
				hidden = false
			case "empty-create":
				first.Review = diffmodel.RenderReviewFile("", "/w/a", "", "draft\n")
				second.Review = diffmodel.RenderReviewFile("/w/a", "/w/a", "draft\n", "")
				hidden = false
			case "empty-delete":
				first.Review = diffmodel.RenderReviewFile("/w/a", "/w/a", "", "draft\n")
				second.Review = diffmodel.RenderReviewFile("/w/a", "", "draft\n", "")
				hidden = false
			}
			attempts := []Attempt{snapshotAttempt("first", "main", "first", first), snapshotAttempt("second", "worker", "second", second)}
			snapshot := snapshotFixture(t, attempts...)
			pane := snapshot.Publication.ForDiff("/w")
			if (len(pane.Files) == 0) != hidden || (len(pane.Unfiltered) == 0) != hidden {
				t.Fatalf("saved pane membership: files=%+v composed=%+v", pane.Files, pane.Unfiltered)
			}
			if !reflect.DeepEqual(pane.Counts, snapshot.Publication.Counts) || !reflect.DeepEqual(slices.Collect(snapshot.Attempts()), attempts) || len(snapshot.Publication.Files) != 1 {
				t.Fatal("filter changed retained history or counts")
			}
			for _, attempt := range attempts {
				detail := snapshot.ActivityDetail(activitydetail.Request{Operation: attempt.Operation})
				if len(detail.Pages) != 1 || detail.Pages[0].Code == "" {
					t.Fatalf("exact-operation evidence lost: %+v", detail)
				}
			}
			if !reflect.DeepEqual(pane.ForDiff("/w"), pane) {
				t.Fatal("saved filtering is not idempotent")
			}
		})
	}
}

func TestSavedDiffReappearsAfterIncrementalEdit(t *testing.T) {
	first := snapshotAttempt("first", "main", "first", netCountsCapture(1, "/root", "/w/a", "/w/a", "old\n", "draft\n"))
	snapshot := snapshotFixture(t, first)
	for i, after := range []string{"old\n", "final\n"} {
		before := "draft\n"
		if i == 1 {
			before = "old\n"
		}
		builder := NewBuilder(snapshot)
		chunk := netCountsCapture(uint64(i+2), "/root/worker", "/w/a", "/w/a", before, after)
		if err := builder.Add(snapshotAttempt(chunk.Key, "worker", chunk.Key, chunk)); err != nil {
			t.Fatal(err)
		}
		snapshot = builder.Finish()
		pane := snapshot.Publication.ForDiff("/w")
		if len(pane.Files) != i || snapshot.Len() != i+2 {
			t.Fatalf("incremental membership: pane=%+v attempts=%d", pane.Files, snapshot.Len())
		}
		if i == 1 {
			file := pane.Unfiltered[pane.Files[0].Key()]
			if len(file.Chunks) != 1 || !strings.Contains(file.Chunks[0].Review.Diff, "+final") || !strings.Contains(file.Chunks[0].Review.Diff, "-old") {
				t.Fatalf("restored net diff = %+v", file)
			}
		}
	}
}

func TestSavedDiffKeepsLinkTypeEvidence(t *testing.T) {
	// Capture renders a symlink target as "-> target\n". A regular file with
	// those bytes produces identical text while the filesystem type changes.
	chunk := netCountsCapture(1, "/root", "/w/a", "/w/a", "-> target\n", "-> target\n")
	chunk.Review.Link = true
	snapshot := snapshotFixture(t, snapshotAttempt("link", "main", "link", chunk))
	pane := snapshot.Publication.ForDiff("/w")
	if len(pane.Files) != 1 || len(pane.Unfiltered) != 1 || !pane.Files[0].Chunks[0].Review.Link {
		t.Fatalf("structural link evidence hidden: %+v", pane)
	}
}
