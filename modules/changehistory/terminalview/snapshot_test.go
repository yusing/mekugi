package terminalview

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func assertRowsSnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
}

func snapshotFiles() []presentation.File {
	const workspace = "/workspace/"
	reviews := []diffmodel.ReviewFile{
		diffmodel.RenderReviewFile("", workspace+"docs/guide.md", "", "# Guide\n"),
		diffmodel.RenderReviewFile(workspace+"src/old.go", workspace+"src/main.go", "package old\n", "package main\n"),
		diffmodel.RenderReviewFile(workspace+"src/obsolete.go", "", "package old\n", ""),
		{BeforePath: workspace + "data/cache.bin", AfterPath: workspace + "data/cache.bin", Incomplete: "baseline unavailable"},
	}
	var files []presentation.File
	for i, review := range reviews {
		path := review.AfterPath
		if path == "" {
			path = review.BeforePath
		}
		caller, change := "/root", "amber1"
		if i > 1 {
			caller, change = "/root/worker", "birch2"
		}
		files = append(files, presentation.File{Path: path, Chunks: []presentation.Chunk{{
			Key: path, CaptureOrder: uint64(i + 1), Review: review,
			Change: change, Caller: caller, Source: "apply_patch",
		}}})
	}
	return files
}

func TestUISnapshotDiffNavigation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		nav           Navigation
		width, height int
	}{
		{"files_tree", Navigation{Focused: true, Cursor: 3}, 42, 12},
		{"files_collapsed", Navigation{Collapsed: map[string]bool{"src": true}}, 30, 8},
		{"files_scrolled", Navigation{Flat: true, Focused: true, Cursor: 3, Top: 2}, 26, 4},
		{"files_filtered", Navigation{Query: "src", Filtering: true}, 30, 7},
		{"files_no_matches", Navigation{Query: "missing", Filtering: true}, 30, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := snapshotFiles()
			var counts []diffmodel.Counts
			for _, file := range files {
				counts = append(counts, file.NetCounts())
			}
			tc.nav.Rebuild(files, "/workspace")
			assertRowsSnapshot(t, tc.name, tc.nav.Render(files, counts, 1, tc.width, tc.height, diffrender.DarkTheme))
		})
	}
}

func TestUISnapshotDiffChanges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		changes       Changes
		width, height int
	}{
		{"changes_expanded", Changes{Expanded: map[string]bool{"amber1": true, "birch2": true}, Cursor: 4}, 60, 12},
		{"changes_narrow", Changes{}, 26, 6},
		{"changes_no_matches", Changes{Query: "@missing"}, 36, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := presentation.View{Files: snapshotFiles()}
			tc.changes.Rebuild(&view, "/workspace")
			assertRowsSnapshot(t, tc.name, tc.changes.Render(true, tc.changes.Query != "", "", tc.width, tc.height, diffrender.DarkTheme))
		})
	}
}

func TestUISnapshotDependencyDirectoryNavigation(t *testing.T) {
	file := presentation.File{Path: "/workspace/node_modules", Chunks: []presentation.Chunk{{Review: diffmodel.ReviewFile{BeforePath: "/workspace/node_modules", AfterPath: "/workspace/node_modules", Directory: true, Incomplete: "directory contents intentionally not captured"}}}}
	files := []presentation.File{file}
	nav := Navigation{Flat: true, Focused: true}
	nav.Rebuild(files, "/workspace")
	assertRowsSnapshot(t, "dependency-directory-navigation", nav.Render(files, []diffmodel.Counts{file.NetCounts()}, 0, 50, 5, diffrender.DarkTheme))
}

func TestUISnapshotWordDiff(t *testing.T) {
	const before = "Keep each listed task small and bounded. Record results as they become known.\n"
	const after = "Keep each listed task small and checkable. At 70% context use, split the current slice. Record results as they become known.\n"
	review := diffmodel.RenderReviewFile("guidance.txt", "guidance.txt", before, after)
	render, err := new(Renderer).Render(t.Context(), diffrender.DarkTheme,
		[]presentation.File{{Path: "guidance.txt", Chunks: []presentation.Chunk{{Review: review}}}}, "", 60)
	if err != nil {
		t.Fatal(err)
	}
	assertRowsSnapshot(t, "word-diff", render.Lines)
}
