package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotMoveActivityAndDialog(t *testing.T) {
	workspace := "/workspace"
	file := diffmodel.RenderReviewFile(workspace+"/internal/router/old.txt.new", workspace+"/internal/router/old.txt", "same\n", "same\n")
	text := presentation.ReceiptText(workspace, receiptEvidence(legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file})}))
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: text, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "move"}}}})
	var rows []string
	for _, block := range activityui.Disclosure(nil).Blocks(activitypresentation.Operations(text)) {
		rows = append(rows, u.shell.Transcript.Painter.Block(block, 100)...)
	}
	uisnapshot.Assert(t, "testdata/snapshots/move-paths-activity.txt", strings.Join(rows, "\n")+"\n")
	c := liveDiffChangesPane(t, 100, 24, nil)
	c.SetWorkspace(workspace)
	u.review.Projection.Data = reviewFixtureSnapshot(t, presentation.Attempt{AttemptIdentity: presentation.AttemptIdentity{Key: presentation.AttemptKey{Workspace: workspace, Call: "move"}, Correlation: "move\x000"}, Operation: activitydetail.Operation{Thread: "main", Item: "move"}, Chunks: []presentation.Chunk{{Workspace: workspace, Review: file}}})
	publishFixtureReview(t, u)
	terminalTest(u).Auxiliary = c
	if !terminalTest(u).openActivityEdit(u.shell.Transcript, 1, pathdisplay.Move(workspace, file.BeforePath, file.AfterPath)) {
		t.Fatal("compressed move endpoint did not open exact captured dialog")
	}
	uisnapshot.Assert(t, "testdata/snapshots/move-paths-dialog.txt", drawOutputDialog(terminalTest(u))+"\n")
}

func TestHostMovePaths(t *testing.T) {
	for _, camelCase := range []bool{false, true} {
		change := binding.FileChange{Path: "/w/internal/old.go"}
		change.Kind.Type = "update"
		if camelCase {
			change.Kind.MovePath2 = "/w/internal/new.go"
		} else {
			change.Kind.MovePath = "/w/internal/new.go"
		}
		item := binding.ThreadItem{Status: "completed", Changes: []binding.FileChange{change}}
		if got := activitypresentation.HostEditText(item.Observation(), "/w"); got != "Move `internal/{old.go=>new.go}` · +0 −0 · apply_patch" {
			t.Fatalf("host move summary = %q", got)
		}
		pages := activitypresentation.HostEditPages(item.Observation(), "/w", "item/completed")
		if len(pages) != 1 || pages[0].Path != "internal/{old.go=>new.go}" || pages[0].Code != "move \"internal/old.go\" -> \"internal/new.go\"\n" {
			t.Fatalf("host move pages = %+v", pages)
		}
	}
}

func TestMChangesMoveWorkspacePaths(t *testing.T) {
	f := newMChangesSliceFixture(t, "relative-moves")
	id := f.reserve(t, f.thread, "move")
	before, after := filepath.Join(f.workspace, "internal", "old.txt"), filepath.Join(f.workspace, "internal", "new.txt")
	file := diffmodel.RenderReviewFile(before, after, "same\n", "same\n")
	f.publish(t, id, "move", "move", legacycall.History{ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{file})})
	for _, mode := range []string{"", "--history", "--net"} {
		stdout, stderr, status := f.run(t, "mchanges "+id+" "+mode)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "move \"internal/old.txt\" -> \"internal/new.txt\"\n") || strings.Contains(stdout, f.workspace) {
			t.Fatalf("%s output = %q, %q, %d", mode, stdout, stderr, status)
		}
	}
}

func TestHostMoveDialogMetadata(t *testing.T) {
	for _, body := range []string{"", "@@ -1 +1 @@\n-/w/source-row\n+/w/source-row\n+Moved to: /w/internal/new.go\n"} {
		change := binding.FileChange{Path: "/w/internal/old.go", Diff: body + "\n\nMoved to: /w/internal/new.go"}
		change.Kind.MovePath = "/w/internal/new.go"
		item := binding.ThreadItem{Status: "completed", Changes: []binding.FileChange{change}}
		pages := activitypresentation.HostEditPages(item.Observation(), "/w", "item/completed")
		want := body + "\n\nMoved to: internal/new.go"
		if len(pages) != 1 || pages[0].Code != want || item.Changes[0].Diff != change.Diff {
			t.Fatalf("host metadata projection = %+v, want %q", pages, want)
		}
	}
}

func TestUISnapshotHostMoveDialogSyntax(t *testing.T) {
	change := binding.FileChange{Path: "/w/internal/old.go", Diff: "@@ -1 +1 @@\n-var answer = 41\n+var answer = 42\n"}
	change.Kind.Type, change.Kind.MovePath = "update", "/w/internal/new.go"
	item := binding.ThreadItem{Status: "completed", Changes: []binding.FileChange{change}}
	pages := activitypresentation.HostEditPages(item.Observation(), "/w", "item/completed")
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.source.Entries = []sessionpresentation.ActivityRecord{{Source: sessionpresentation.ActivityEntry{Seq: 1, Native: &sessionpresentation.NativeItem{Thread: "main", Item: "move", EditPages: pages}}}}
	if !terminalTest(u).openActivityEdit(u.shell.Transcript, 1, "internal/{old.go=>new.go}") {
		t.Fatal("compressed host move did not open its exact page")
	}
	frame := drawOutputDialog(terminalTest(u))
	var renderer diffrender.Renderer
	want, err := renderer.ColorDiffPath(t.Context(), diffrender.DarkTheme, "internal/new.go", change.Diff)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range want {
		if row == "" {
			continue
		}
		found := false
		for _, line := range terminalTest(u).Dialog.Document.Lines {
			if line.Text == row {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("compressed move label lost source token colors: missing %q", row)
		}
	}
	if pages[0].Verb != "Move" || terminalTest(u).Dialog.Document.Text != change.Diff {
		t.Fatal("move action or copied source changed")
	}
	uisnapshot.Assert(t, "testdata/snapshots/host-move-dialog-syntax.txt", frame+"\n")
}
