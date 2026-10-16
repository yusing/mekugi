package presentation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestReviewSelectionAndQueriesWithoutRenderer(t *testing.T) {
	first := netCountsCapture(1, "/root", "/workspace/src/a.go", "/workspace/src/a.go", "old\n", "middle\n")
	second := netCountsCapture(2, "/root/worker", "/workspace/src/a.go", "/workspace/src/b.go", "middle\n", "final\n")
	var view View
	view.Merge(GroupCaptures([]Chunk{first}))
	selected := view.Files[view.Selected].Key()
	view.Merge(GroupCaptures([]Chunk{second, first}))
	view.FilterCaller("/root/worker")
	file := view.Visible[selected]
	if view.Files[view.Selected].Key() != selected || file.Path != "/workspace/src/b.go" || file.Baseline != 1 ||
		len(file.Origins) != 1 || file.Origins[0].Caller != "/root/worker" ||
		file.NetCounts() != (diffmodel.Counts{Added: 1, Removed: 1}) {
		t.Fatalf("selection, caller baseline or provenance changed: %+v", file)
	}
	if len(file.Chunks) != 1 || !strings.Contains(file.Chunks[0].Review.Diff, "-old\n+final") {
		t.Fatalf("filtered review lost the captured original baseline: %+v", file.Chunks)
	}
	changes := Changes{Query: "@writer"}
	changes.Rebuild(&view, "/workspace", func(caller string) string {
		if caller == "/root/worker" {
			return "Writer"
		}
		return "Main"
	})
	if len(changes.Nodes) != 1 || changes.Nodes[0].Change != second.Change || len(changes.Nodes[0].Files) != 1 {
		t.Fatalf("caller query did not consume supplied display names: %+v", changes.Nodes)
	}
	tree := FileTree{Query: "b.go", Collapsed: map[string]bool{"src": true}}
	tree.Rebuild([]File{file}, "/workspace")
	if len(tree.Entries) != 1 || tree.Entries[0].File != 0 || tree.Entries[0].Label != "src/b.go" {
		t.Fatalf("path query hid a matching collapsed descendant: %+v", tree.Entries)
	}
	tree.Query = ""
	tree.Rebuild([]File{file}, "/workspace")
	if len(tree.Entries) != 1 || tree.Entries[0].Folder != "src" || !tree.Collapsed["src"] {
		t.Fatalf("clearing the path query lost tree expansion: %+v", tree)
	}
}
