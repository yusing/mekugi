package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotMoveActivityAndDialog(t *testing.T) {
	workspace := "/workspace"
	file := mekugi.RenderReviewFile(workspace+"/internal/router/old.txt.new", workspace+"/internal/router/old.txt", "same\n", "same\n")
	text := editReceiptText(workspace, mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}})
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.painter.Theme = livediff.DarkTheme
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: text, native: &liveActivityNativeItem{thread: "main", item: "move"}}}})
	var rows []string
	for _, block := range toolOperationBlocks(text) {
		rows = append(rows, u.view.painter.Block(block, 100)...)
	}
	uisnapshot.Assert(t, "testdata/snapshots/move-paths-activity.txt", strings.Join(rows, "\n")+"\n")
	c := liveDiffChangesController(t, 100, 24, nil)
	c.workspace, c.data = workspace, newLiveDiffData()
	c.data.order = []string{"move"}
	c.data.attempts["move"] = liveDiffAttempt{thread: "main", correlation: "move\x000", chunks: []livediff.Chunk{{Workspace: workspace, Review: file}}}
	u.shell.diff = c
	if !u.shell.openActivityEdit(u.view, 1, pathdisplay.Move(workspace, file.BeforePath, file.AfterPath)) {
		t.Fatal("compressed move endpoint did not open exact captured dialog")
	}
	uisnapshot.Assert(t, "testdata/snapshots/move-paths-dialog.txt", drawOutputDialog(u.shell)+"\n")
}

func TestHostMovePaths(t *testing.T) {
	for _, camelCase := range []bool{false, true} {
		change := appServerFileChange{Path: "/w/internal/old.go"}
		change.Kind.Type = "update"
		if camelCase {
			change.Kind.MovePath2 = "/w/internal/new.go"
		} else {
			change.Kind.MovePath = "/w/internal/new.go"
		}
		item := appServerItem{Status: "completed", Changes: []appServerFileChange{change}}
		if got := appServerEditText(item, "/w"); got != "Move `internal/{old.go=>new.go}` · +0 −0 · apply_patch" {
			t.Fatalf("host move summary = %q", got)
		}
		pages := appServerEditPages(item, "/w", "item/completed")
		if len(pages) != 1 || pages[0].Path != "internal/{old.go=>new.go}" || pages[0].Code != "move \"internal/old.go\" -> \"internal/new.go\"\n" {
			t.Fatalf("host move pages = %+v", pages)
		}
	}
}

func TestMChangesMoveWorkspacePaths(t *testing.T) {
	f := newMChangesSliceFixture(t, "relative-moves")
	id := f.reserve(t, f.thread, "move")
	before, after := filepath.Join(f.workspace, "internal", "old.txt"), filepath.Join(f.workspace, "internal", "new.txt")
	file := mekugi.RenderReviewFile(before, after, "same\n", "same\n")
	f.publish(t, id, "move", "move", mekugiHistory{ReviewFiles: []mekugi.ReviewFile{file}})
	for _, mode := range []string{"", "--history", "--net"} {
		stdout, stderr, status := f.run(t, "mchanges "+id+" "+mode)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "move \"internal/old.txt\" -> \"internal/new.txt\"\n") || strings.Contains(stdout, f.workspace) {
			t.Fatalf("%s output = %q, %q, %d", mode, stdout, stderr, status)
		}
	}
}

func TestHostMoveDialogMetadata(t *testing.T) {
	for _, body := range []string{"", "@@ -1 +1 @@\n-/w/source-row\n+/w/source-row\n+Moved to: /w/internal/new.go\n"} {
		change := appServerFileChange{Path: "/w/internal/old.go", Diff: body + "\n\nMoved to: /w/internal/new.go"}
		change.Kind.MovePath = "/w/internal/new.go"
		item := appServerItem{Status: "completed", Changes: []appServerFileChange{change}}
		pages := appServerEditPages(item, "/w", "item/completed")
		want := body + "\n\nMoved to: internal/new.go"
		if len(pages) != 1 || pages[0].Code != want || item.Changes[0].Diff != change.Diff {
			t.Fatalf("host metadata projection = %+v, want %q", pages, want)
		}
	}
}
