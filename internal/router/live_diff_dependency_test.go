package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
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
				file := mekugi.RenderIncompleteReviewFile("/w/plugins/node_modules", "/w/plugins/node_modules", "dependency directory metadata capture incomplete")
				file.OriginNote = execInventoryNote
				call := fmt.Sprintf("command-%d", i)
				id, err := store.reserveChange(t.Context(), "/w", "stock-thread", call)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				history := mekugiHistory{ChangeID: id, CorrelationID: call, Caller: "/root", ExecutingThread: "stock-thread", Source: "exec",
					ExecOutcome: &execOutcome{Coverage: execCoveragePartial}, ReviewFiles: []mekugi.ReviewFile{file}}
				if err := store.put(t.Context(), "/w", map[string]mekugiHistory{call: history}); err != nil {
					t.Fatal(err)
				}
			}
			reader := &mekugiReplayStore{directory: store.directory}
			scope := liveDiffScope{Workspaces: map[string]map[string]bool{"/w": {"stock-thread": true}}}
			data, err := reader.liveDiffSnapshot(t.Context(), scope)
			if err != nil || len(data.files()) != 0 {
				t.Fatalf("observation-only records became files: %+v, %v", data, err)
			}
			for _, attempt := range data.attempts {
				if attempt.receipt != nil {
					t.Fatal("legacy observation generated an edit receipt")
				}
			}
			for _, mode := range []string{"", "summary", "history"} {
				out, err := reader.readChanges(t.Context(), changeReadOptions{workspace: "/w", ids: ids, view: mode})
				if err != nil || strings.Contains(out, "node_modules") != (mode == "history") || !strings.Contains(out, "incomplete") {
					t.Fatalf("%s lost coverage or exposed observation as edit: %q, %v", mode, out, err)
				}
			}
			putTestChange(t, t.Context(), store, "/w", "ignored", mekugi.RenderReviewFile("/w/FIXME.md", "/w/FIXME.md", "before\n", "after\n"))
			if confirmed {
				for i := range 10 {
					putTestChange(t, t.Context(), store, "/w", fmt.Sprintf("install-%d", i), mekugi.ReviewFile{
						BeforePath: "/w/plugins/node_modules", AfterPath: "/w/plugins/node_modules", Directory: true,
						Incomplete: "directory contents intentionally not captured", OriginNote: execInventoryNote,
					})
				}
			}
			if err := data.reconcile(t.Context(), reader, scope); err != nil {
				t.Fatal(err)
			}
			var captures []livediff.Chunk
			for _, file := range data.files() {
				captures = append(captures, file.Chunks...)
			}
			c := liveDiffChangesController(t, 100, 25, captures)
			c.theme = livediff.DarkTheme
			c.frame(t)
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/saved-diff-dependency-%t.txt", confirmed), strings.Join(c.lines, "\n")+"\n")
			for _, file := range c.view.Visible {
				if file.Path == "/w/plugins/node_modules" && len(file.Chunks) != 1 {
					t.Fatalf("confirmed directory captures not collapsed: %+v", file)
				}
			}
		})
	}
}

func TestLiveDiffTerminalDependencyHistory(t *testing.T) {
	w := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w, "node_modules")
	for i := range 10 {
		gap := mekugi.RenderIncompleteReviewFile(path, path, "dependency directory metadata capture incomplete")
		gap.OriginNote = execInventoryNote
		putTestChange(t, t.Context(), store, w, fmt.Sprintf("gap-%d", i), gap)
		putTestChange(t, t.Context(), store, w, fmt.Sprintf("install-%d", i), mekugi.ReviewFile{
			BeforePath: path, AfterPath: path, Directory: true, Incomplete: "directory contents intentionally not captured",
		})
	}
	connection, _, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{w: {"stock-thread": true}}})
	ui := startLiveDiffTerminal(t, w, store.directory, connection, 24)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	ui.write(t, "v")
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "M node_modules/") })
	if strings.Contains(frame, "metadata capture incomplete") || strings.Contains(frame, "contents intentionally not captured") {
		t.Fatal("saved terminal still renders dependency diagnostic rows")
	}
	var rows []string
	for row := 1; row <= 24; row++ {
		rows = append(rows, liveDiffFrameRow(frame, row))
	}
	t.Log("saved dependency terminal frame:\n" + strings.Join(rows, "\n"))
}

func TestLiveDiffDirectoryLifecycleAndCallerFilter(t *testing.T) {
	for _, paths := range [][4]string{
		{"", "/w/node_modules", "/w/node_modules", ""},
		{"/w/node_modules", "", "", "/w/node_modules"},
	} {
		var captures []livediff.Chunk
		for i := range 2 {
			captures = append(captures, livediff.Chunk{Key: fmt.Sprint(i), CaptureOrder: uint64(i + 1),
				Origin: livediff.Origin{Caller: fmt.Sprintf("/root/agent%d", i)},
				Review: mekugi.ReviewFile{BeforePath: paths[2*i], AfterPath: paths[2*i+1], Directory: true}})
		}
		view := livediff.View{}
		view.Merge(livediff.GroupCaptures(captures))
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
