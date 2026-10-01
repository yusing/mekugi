package diffview

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func assertRowsSnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
}

func snapshotFiles() []livediff.File {
	const workspace = "/workspace/"
	reviews := []mekugi.ReviewFile{
		mekugi.RenderReviewFile("", workspace+"docs/guide.md", "", "# Guide\n"),
		mekugi.RenderReviewFile(workspace+"src/old.go", workspace+"src/main.go", "package old\n", "package main\n"),
		mekugi.RenderReviewFile(workspace+"src/obsolete.go", "", "package old\n", ""),
		{BeforePath: workspace + "data/cache.bin", AfterPath: workspace + "data/cache.bin", Incomplete: "baseline unavailable"},
	}
	var files []livediff.File
	for i, review := range reviews {
		path := review.AfterPath
		if path == "" {
			path = review.BeforePath
		}
		caller, change := "/root", "amber1"
		if i > 1 {
			caller, change = "/root/worker", "birch2"
		}
		files = append(files, livediff.File{Path: path, Chunks: []livediff.Chunk{{
			Key: path, CaptureOrder: uint64(i + 1), Review: review,
			Origin: livediff.Origin{Change: change, Caller: caller, Source: "apply_patch"},
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
			var counts []livediff.Counts
			for _, file := range files {
				counts = append(counts, file.NetCounts())
			}
			tc.nav.Rebuild(files, "/workspace")
			assertRowsSnapshot(t, tc.name, tc.nav.Render(files, counts, 1, tc.width, tc.height, livediff.DarkTheme))
		})
	}
}

func TestUISnapshotDiffChanges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		changes       liveDiffChanges
		width, height int
	}{
		{"changes_expanded", liveDiffChanges{Expanded: map[string]bool{"amber1": true, "birch2": true}, Cursor: 4}, 60, 12},
		{"changes_narrow", liveDiffChanges{}, 26, 6},
		{"changes_no_matches", liveDiffChanges{Query: "@missing"}, 36, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := livediff.View{Files: snapshotFiles()}
			tc.changes.Rebuild(&view, "/workspace")
			assertRowsSnapshot(t, tc.name, tc.changes.Render(true, tc.changes.Query != "", "", tc.width, tc.height, livediff.DarkTheme))
		})
	}
}

func TestUISnapshotDiffPreview(t *testing.T) {
	edit := Preview{ID: "edit", Workspace: "/workspace", Caller: "/root/worker", Status: PreviewEdit,
		Files: []mekugi.ReviewFile{mekugi.RenderReviewFile("/workspace/src/main.go", "/workspace/src/main.go", "package main\n\nconst label = \"old\"\n", "package main\n\nconst label = \"a long replacement that wraps inside a narrow preview\"\n")}}
	pending := Preview{ID: "pending", Workspace: "/workspace", Caller: "/root", Status: PreviewPending,
		Input: "src/main.go\ndocs/guide.md", Footer: "may write · 2 scoped paths"}
	completed := edit
	completed.Complete = true
	observed := edit
	observed.Status = PreviewRunning
	for _, tc := range []struct {
		name          string
		previews      []Preview
		width, height int
	}{
		{"preview_edit_wrapped", []Preview{edit}, 36, 8},
		{"preview_completed", []Preview{completed}, 72, 7},
		{"preview_observed", []Preview{observed}, 72, 7},
		{"preview_pending_footer", []Preview{pending}, 48, 7},
		{"preview_unavailable", []Preview{{ID: "unavailable", Workspace: "/workspace", Caller: "/root", Status: PreviewUnavailable + "baseline unavailable"}}, 48, 4},
		{"preview_concurrent", []Preview{pending, edit}, 60, 12},
		{"preview_accordion", []Preview{pending, edit}, 42, 7},
		{"preview_hidden_calls", []Preview{pending, edit}, 42, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Prefer a caller explicitly; never choose the open card by wall-clock arrival time.
			pane := PreviewPane{Prefer: "/root/worker"}
			for _, preview := range tc.previews {
				pane.Update(preview)
			}
			rows, err := pane.Render(t.Context(), "/workspace", livediff.DarkTheme, tc.width, tc.height)
			if err != nil {
				t.Fatal(err)
			}
			assertRowsSnapshot(t, tc.name, rows)
		})
	}
}

func TestUISnapshotDependencyDirectoryNavigation(t *testing.T) {
	file := livediff.File{Path: "/workspace/node_modules", Chunks: []livediff.Chunk{{Review: mekugi.ReviewFile{BeforePath: "/workspace/node_modules", AfterPath: "/workspace/node_modules", Directory: true, Incomplete: "directory contents intentionally not captured"}}}}
	files := []livediff.File{file}
	nav := Navigation{Flat: true, Focused: true}
	nav.Rebuild(files, "/workspace")
	assertRowsSnapshot(t, "dependency-directory-navigation", nav.Render(files, []livediff.Counts{file.NetCounts()}, 0, 50, 5, livediff.DarkTheme))
}
