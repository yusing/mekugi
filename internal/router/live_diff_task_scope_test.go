package router

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotLiveDiffTaskScope(t *testing.T) {
	for _, width := range []int{70, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			var captures []livediff.Chunk
			for i, task := range []string{"/1/1", "/1/2", "/2/1", "", "/1"} {
				caller := "/root"
				if i == 4 {
					caller = "/root/worker"
				}
				chunk := liveDiffCapture(fmt.Sprint(i), fmt.Sprintf("src/%d.go", i), uint64(i+1), "old\n", "new\n",
					livediff.Origin{Change: fmt.Sprintf("amber%d", i+1), Caller: caller, Source: "apply_patch"})
				chunk.TaskPath = task
				captures = append(captures, chunk)
			}
			c := liveDiffChangesController(t, width, 18, captures)
			c.native, c.navigation.Focused = true, true
			journal := threadJournal{Author: "/root", Items: []journalItem{
				{Path: "/1", Kind: "task", State: "working", Updated: 9},
				{Path: "/1/1", Kind: "task", State: "done", Updated: 3},
				{Path: "/1/2", Kind: "task", State: "working", Updated: 4, Agent: "/root/worker"},
			}}
			c.updateTaskScope(&journal)
			for scope, want := range []int{2, 3, 5} {
				for _, changes := range []bool{false, true} {
					c.navigation.ChangesTab = changes
					var output bytes.Buffer
					c.stdout = &output
					c.frame(t)
					if len(c.navigation.Matches) != want || len(c.navigation.Changes.Nodes) != want {
						t.Fatalf("scope %d: files=%d changes=%d want=%d", scope, len(c.navigation.Matches), len(c.navigation.Changes.Nodes), want)
					}
					left, right := c.nativeTitle()
					rows := []string{left + " | " + right}
					for row := 1; row <= 18; row++ {
						rows = append(rows, liveDiffFrameRow(output.String(), row))
					}
					uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/live-diff-task-scope-%d-%d-%t.txt", width, scope, changes), strings.Join(rows, "\n"))
					c.handleKey('t') // Flat Files must use the same scoped set.
					c.frame(t)
					if len(c.navigation.Matches) != want {
						t.Fatal("tree toggle changed scoped file membership")
					}
				}
				c.handleKey('c')
			}
			if c.taskScope != liveDiffSubslice || len(c.navigation.Matches) != 2 {
				t.Fatal("scope cycle did not return to subslice")
			}
			c.filterCaller("/root/worker")
			if len(c.navigation.Changes.Nodes) != 1 || len(c.navigation.Matches) != 1 {
				t.Fatal("caller filter did not intersect task scope")
			}
			c.resetScope()
			if c.taskScope != liveDiffSubslice || c.view.TaskPaths != nil {
				t.Fatal("session switch retained task scope")
			}
		})
	}
}

func TestUISnapshotLiveDiffTaskScopeRetainsAttributionAndBaseline(t *testing.T) {
	proxy, _ := treeTestJournal(t)
	workspace := t.TempDir()
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "tree", "/root", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("First"), State: new("working")})
	store := proxy.replayStore
	before := "old\n"
	for i, after := range []string{"draft\n", "final\n"} {
		call := fmt.Sprintf("edit-%d", i)
		id, err := store.reserveChange(t.Context(), workspace, "tree", call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, Caller: "/root", ExecutingThread: "tree",
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(workspace+"/a.go", workspace+"/a.go", before, after)}}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")},
				journalMutation{Op: "add", Kind: "task", Title: new("Second"), State: new("working")})
			// Confirmation after a task transition must keep the first attribution.
			if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
				t.Fatal(err)
			}
		}
		before = after
	}
	reader := &mekugiReplayStore{directory: store.directory}
	data, err := reader.liveDiffSnapshot(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"tree": true}}})
	if err != nil {
		t.Fatal(err)
	}
	c := liveDiffChangesController(t, 120, 18, nil)
	c.view.Merge(data.files())
	journal := treeSnapshot(t, proxy, workspace)
	c.updateTaskScope(&journal)
	file := c.view.Visible[c.view.Files[0].Key()]
	if file.Baseline != 1 || len(file.Origins) != 1 || len(file.Chunks) != 1 || file.Chunks[0].Review.Diff != mekugi.RenderReviewFile(workspace+"/a.go", workspace+"/a.go", "draft\n", "final\n").Diff {
		t.Fatalf("scoped composition lost its baseline: %+v", file)
	}
	if got := liveDiffNetCounts(&c.view); got == nil || *got != (livediff.Counts{Added: 1, Removed: 1}) {
		t.Fatalf("task scope changed project counts: %+v", got)
	}
	paths := []string{}
	for _, chunk := range c.view.Files[0].Chunks {
		paths = append(paths, chunk.TaskPath)
	}
	if !slices.Equal(paths, []string{"/1", "/2"}) {
		t.Fatalf("restored attribution = %v", paths)
	}
	c.workspace = workspace
	c.frame(t)
	uisnapshot.Assert(t, "testdata/snapshots/live-diff-task-baseline.txt", strings.Join(c.rendering.Lines, "\n"))
}

func TestNativeDiffTaskScopeTracksJournal(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/w")
	j := threadJournal{Author: "/root", Items: []journalItem{
		{Path: "/1/1", Kind: "task", State: "done", Updated: 1},
		{Path: "/1/2", Kind: "task", State: "working", Updated: 2},
	}}
	u.journal = &nativeJournalSink{tree: &j}
	u.shell.side, u.shell.diffOpen, u.shell.focus = true, true, 1
	c := u.shell.diff
	var captures []livediff.Chunk
	for i, task := range []string{"/1/1", "/1/2", "/2"} {
		chunk := liveDiffCapture(fmt.Sprint(i), fmt.Sprintf("%d.go", i), uint64(i+1), "old\n", "new\n", livediff.Origin{Caller: "/root"})
		chunk.TaskPath = task
		captures = append(captures, chunk)
	}
	c.view.Merge(livediff.GroupCaptures(captures))
	c.view.RefreshVisible()
	paint := func(want int) {
		t.Helper()
		if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
			t.Fatal(err)
		}
		if len(c.navigation.Matches) != want {
			t.Fatalf("native scoped files = %d, want %d", len(c.navigation.Matches), want)
		}
	}
	paint(1)
	c.handleKey('c')
	paint(2)
	c.handleKey('c')
	paint(3)
	c.handleKey('c')
	j.Items[1].State = "done"
	j.Items = append(j.Items, journalItem{Path: "/2", Kind: "task", State: "working", Updated: 3})
	paint(1)
	if c.navigation.Scope != "subslice /2" || c.view.Files[c.view.Selected].Path != "/w/2.go" {
		t.Fatalf("native task transition did not select its diff: scope=%s selected=%d", c.navigation.Scope, c.view.Selected)
	}
}
