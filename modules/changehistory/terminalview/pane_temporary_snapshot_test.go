package terminalview

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotSavedDiffTemporaryEdits(t *testing.T) {
	for _, width := range []int{70, 120} {
		for _, changes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-changes-%t", width, changes), func(t *testing.T) {
				var captures []presentation.Chunk
				for i, path := range []string{"/tmp/scratch", "/w/main.go", "/tmp/log", "/tmpfoo/kept.go"} {
					captures = append(captures, presentation.Chunk{Key: path, Workspace: "/w", CaptureOrder: uint64(i + 1),
						Change: fmt.Sprintf("amber%d", i+1), Caller: "/root",
						Review: diffmodel.RenderReviewFile("", path, "", "new source\n")})
				}
				var publication presentation.Publication
				// A mixed change keeps only its non-temporary file and counts.
				captures[0].Change = captures[1].Change
				publication = publication.With(presentation.GroupCaptures(captures))
				pane := NewPane("/w", diffrender.DarkTheme)
				pane.Update(publication, "captured")
				pane.lastWidth, pane.lastHeight = width, 18
				pane.frame(t)
				pane.Key("s")
				if changes {
					pane.Key("\t")
				}
				var output bytes.Buffer
				pane.paintTo(t, &output)
				if len(pane.view.Files) != 2 || len(pane.navigation.Changes.Nodes) != 2 {
					t.Fatal("temporary captures remained in Files or Changes")
				}
				source := pane.Selection(width-2, 17)
				if len(source.Document) == 0 || strings.Contains(strings.Join(source.Document, "\n"), "/tmp/") {
					t.Fatal("temporary captures remained in selectable diff content")
				}
				var rows []string
				for row := 1; row <= 18; row++ {
					rows = append(rows, liveDiffFrameRow(output.String(), row))
				}
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/saved-diff-temporary-%d-changes-%t.txt", width, changes), strings.Join(rows, "\n"))
				selected := pane.view.Files[pane.view.Selected].Key()
				pane.Update(publication, "captured")
				pane.frame(t)
				if pane.view.Files[pane.view.Selected].Key() != selected {
					t.Fatal("filtered publication changed selection")
				}
				pane.Update(publication.With(presentation.GroupCaptures(captures[:1])), "captured")
				pane.frame(t)
				if len(pane.view.Files) != 0 || len(pane.navigation.Changes.Nodes) != 0 {
					t.Fatal("temporary-only replacement retained stale navigation")
				}
			})
		}
	}
}

func TestUISnapshotSavedDiffRevertedEdits(t *testing.T) {
	for _, state := range []string{"files", "changes", "empty"} {
		t.Run(state, func(t *testing.T) {
			captures := []presentation.Chunk{
				liveDiffCapture("one", "reverted.go", 1, "original\n", "discarded\n", presentation.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
				liveDiffCapture("two", "retained.go", 2, "before\n", "retained edit\n", presentation.Origin{Change: "amber2", Caller: "/root", Source: "apply_patch"}),
				liveDiffCapture("three", "reverted.go", 3, "discarded\n", "original\n", presentation.Origin{Change: "amber3", Caller: "/root", Source: "exec_command"}),
			}
			pane := NewPane("/w", diffrender.DarkTheme)
			var publication presentation.Publication
			publication = publication.With(presentation.GroupCaptures(captures[:2]))
			pane.Update(publication, "captured")
			pane.lastWidth, pane.lastHeight = 120, 18
			pane.frame(t)
			pane.Key("s")
			if state == "changes" {
				pane.Key("\t")
			}
			if state == "empty" {
				captures = append(captures, liveDiffCapture("four", "retained.go", 4, "retained edit\n", "before\n", presentation.Origin{Change: "amber4", Caller: "/root", Source: "exec_command"}))
			}
			publication = publication.With(presentation.GroupCaptures(captures))
			pane.Update(publication, "captured")
			pane.frame(t)
			var output bytes.Buffer
			pane.paintTo(t, &output)
			var rows []string
			for row := 1; row <= 18; row++ {
				rows = append(rows, liveDiffFrameRow(output.String(), row))
			}
			rendered := strings.Join(rows, "\n")
			for _, absent := range []string{"reverted.go", "discarded", "amber1", "amber3"} {
				if strings.Contains(rendered, absent) {
					t.Fatalf("reverted file or provenance remains on screen: %q\n%s", absent, rendered)
				}
			}
			if state != "empty" && (!strings.Contains(rendered, "retained.go") || !strings.Contains(rendered, "retained edit") || !strings.Contains(rendered, "amber2")) {
				t.Fatalf("retained file, content or provenance missing from screen:\n%s", rendered)
			}
			if state == "empty" && (strings.Contains(rendered, "retained.go") || strings.Contains(rendered, "amber2") || strings.Contains(rendered, "amber4")) {
				t.Fatalf("all-reverted screen retained stale files or provenance:\n%s", rendered)
			}
			uisnapshot.Assert(t, "testdata/snapshots/saved-diff-reverted-"+state+".txt", rendered)
		})
	}
}
