package router

import (
	"fmt"
	"strings"
	"testing"

	sessionreview "github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/presentation"

	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Reproduce the reported session's ten observation-only records, then reopen
// the store as a fresh saved-Diff reader. No workspace files are consulted.
func TestUISnapshotSavedDiffDependencyHistory(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmed-%t", confirmed), func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for i := range 10 {
				file := diffmodel.RenderIncompleteReviewFile("/w/plugins/node_modules", "/w/plugins/node_modules", "dependency directory metadata capture incomplete")
				file.OriginNote = changecapture.InventoryNote
				call := fmt.Sprintf("command-%d", i)
				id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), "/w", "stock-thread", call)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				history := mekugiHistory{ChangeID: id, CorrelationID: call, Caller: "/root", ExecutingThread: "stock-thread", Source: "exec",
					ExecOutcome: &changecapture.Outcome{Coverage: changecapture.CoveragePartial}, ReviewFiles: []diffmodel.ReviewFile{file}}
				if err := store.put(t.Context(), "/w", map[string]mekugiHistory{call: history}); err != nil {
					t.Fatal(err)
				}
			}
			reader := testReplayStore(mekugiReplayStore{directory: store.directory})
			scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{"/w": {"stock-thread": true}}}
			data, err := reader.liveDiffSnapshot(t.Context(), scope)
			if err != nil || len(data.Publication.Files) != 0 {
				t.Fatalf("observation-only records became files: %+v, %v", data, err)
			}
			for attempt := range data.Attempts() {
				if attempt.Receipt != nil {
					t.Fatal("legacy observation generated an edit receipt")
				}
			}
			for _, mode := range []string{"", "summary", "history"} {
				out, err := reader.changes.Read(t.Context(), reader.scoped(t.Context()).changeScope(), changes.Query{Workspace: "/w", IDs: ids, View: mode})
				if err != nil || strings.Contains(out, "node_modules") != (mode == "history") || strings.Contains(out, "incomplete") != (mode == "history") {
					t.Fatalf("%s lost coverage or exposed observation as edit: %q, %v", mode, out, err)
				}
			}
			putTestChange(t, t.Context(), store, "/w", "ignored", diffmodel.RenderReviewFile("/w/FIXME.md", "/w/FIXME.md", "before\n", "after\n"))
			if confirmed {
				for i := range 10 {
					putTestChange(t, t.Context(), store, "/w", fmt.Sprintf("install-%d", i), diffmodel.ReviewFile{
						BeforePath: "/w/plugins/node_modules", AfterPath: "/w/plugins/node_modules", Directory: true,
						Incomplete: "directory contents intentionally not captured", OriginNote: changecapture.InventoryNote,
					})
				}
			}
			projection := sessionreview.Projection{Changes: reader.changes, Retained: reader.changeScope(), Data: data}
			if _, err := projection.Apply(t.Context(), sessionreview.Event{Kind: "scope", Scope: &scope}); err != nil {
				t.Fatal(err)
			}
			data = projection.Data
			var captures []presentation.Chunk
			for _, file := range data.Publication.Files {
				captures = append(captures, file.Chunks...)
			}
			c := liveDiffChangesPane(t, 100, 25, captures)
			c.SetTheme(diffrender.DarkTheme)
			paintSavedDiff(t, c, 100, 25)
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/saved-diff-dependency-%t.txt", confirmed), strings.Join(c.Selection(99, 24).Document, "\n")+"\n")
			view := presentation.View{}
			view.Merge(data.Publication.Files)
			view.RefreshVisible()
			for _, file := range view.Visible {
				if file.Path == "/w/plugins/node_modules" && len(file.Chunks) != 1 {
					t.Fatalf("confirmed directory captures not collapsed: %+v", file)
				}
			}
		})
	}
}

func TestLiveDiffDirectoryLifecycleAndCallerFilter(t *testing.T) {
	var viewPublication presentation.Publication
	for _, paths := range [][4]string{
		{"", "/w/node_modules", "/w/node_modules", ""},
		{"/w/node_modules", "", "", "/w/node_modules"},
	} {
		var captures []presentation.Chunk
		for i := range 2 {
			captures = append(captures, presentation.Chunk{Key: fmt.Sprint(i), CaptureOrder: uint64(i + 1),
				Origin: presentation.Origin{Caller: fmt.Sprintf("/root/agent%d", i)},
				Review: diffmodel.ReviewFile{BeforePath: paths[2*i], AfterPath: paths[2*i+1], Directory: true}})
		}
		view := changeview.View{}
		viewPublication = viewPublication.With(presentation.GroupCaptures(captures))
		view.Update(viewPublication)
		view.RefreshVisible()
		visible := view.Visible[view.Files[0].Key()]
		if paths[0] == "" {
			if len(visible.Chunks) != 0 {
				t.Fatalf("created/deleted directory remains visible: %+v", visible)
			}
		} else if len(visible.Chunks) != 1 || visible.Chunks[0].Review.BeforePath != paths[0] || visible.Chunks[0].Review.AfterPath != paths[3] {
			t.Fatalf("recreated directory lost its status: %+v", visible)
		}
		view.FilterCaller("/root/agent1")
		visible = view.Visible[view.Files[0].Key()]
		if len(visible.Chunks) != 1 || visible.Chunks[0].Review.BeforePath != paths[2] || visible.Chunks[0].Review.AfterPath != paths[3] {
			t.Fatalf("caller filter combined another caller's directory endpoints: %+v", visible)
		}
	}
}
