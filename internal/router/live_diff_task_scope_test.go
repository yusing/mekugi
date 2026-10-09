package router

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotNativeDiffMountedChildren(t *testing.T) {
	s, root, client := runtimeJournalFixture(t)
	child, grandchild, sibling := root, root, root
	child.Agent, grandchild.Agent, sibling.Agent = "child", "nested", "sibling"
	for _, binding := range []ObservationBinding{child, grandchild, sibling} {
		runtimeJournalBind(t, s, binding, client)
		runtimeJournalAdd(t, s, binding, client, binding.Agent+"-task", binding.Agent+" work")
	}
	input := `{"journal":[{"op":"add","kind":"task","title":"Slice","state":"working"},{"op":"add","under":"/1","kind":"task","title":"Selected","state":"working"},{"op":"add","under":"/1","kind":"task","title":"Other","state":"done"}]}`
	runtimeJournalReceipt(t, s, root, client, "root-tasks", "journal_batch", input)
	runtimeJournalInvoke(t, s, client, "root-tasks", "journal_batch", input)
	var calls []ObservationCall
	for _, binding := range []ObservationBinding{root, child, grandchild, sibling} {
		name := binding.Agent
		if name == "" {
			name = "main"
		}
		path := filepath.Join(root.Workspace, name+".txt")
		call := ObservationCall{Binding: binding, ID: name + "-write", Tool: "Write", Input: `{}`, Paths: []string{path}}
		if err := s.owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
		nativeObservationWrite(t, path, name+" effect\n")
		if _, err := s.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	c := liveDiffChangesController(t, 120, 18, nil)
	c.store, c.workspace, c.native = s.owner.store, root.Workspace, true
	sub := s.owner.broker.subscribe()
	defer s.owner.broker.unsubscribe(sub)
	apply := func() {
		t.Helper()
		for _, event := range s.owner.broker.takePreviews(sub) {
			if _, err := c.applyEvent(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		c.updateTaskScope(s.journal.sink().presented())
		c.frame(t)
	}
	apply()
	if len(c.navigation.Matches) != 1 {
		t.Fatal("unproven native children entered mounted scope")
	}
	c.taskScope = liveDiffAll
	c.updateTaskScope(s.journal.sink().presented())
	c.filterCaller("native/child")
	c.navigation.Focused = true
	c.back = liveDiffBack{kind: 'b', caller: "native/nested"}
	nativeParentReceipt(t, s, client, child, "spawn-nested")
	nativeParentReceipt(t, s, client, root, "spawn-child")
	nativeParentReceipt(t, s, client, root, "spawn-sibling")
	for _, proof := range [][2]string{{"spawn-nested", grandchild.Agent}, {"spawn-child", child.Agent}, {"spawn-sibling", sibling.Agent}} {
		if err := s.journal.parent(t.Context(), proof[0], proof[1], "completed"); err != nil {
			t.Fatal(err)
		}
	}
	mount := `{"journal":[{"op":"set","p":"/1/1","agent":"/root/child"},{"op":"set","p":"/1/2","agent":"/root/sibling"}]}`
	runtimeJournalReceipt(t, s, root, client, "mount-children", "journal_batch", mount)
	runtimeJournalInvoke(t, s, client, "mount-children", "journal_batch", mount)
	apply()
	if c.view.Caller != "/root/child" || c.back.caller != "/root/child/nested" || len(c.navigation.Matches) != 1 {
		t.Fatal("ancestry refresh lost active or saved child selection")
	}
	c.escapeKey()
	c.frame(t)
	if c.view.Caller != "/root/child/nested" || len(c.navigation.Matches) != 1 {
		t.Fatal("back navigation restored a retired provisional caller")
	}
	c.filterCaller("")
	c.taskScope = liveDiffSubslice
	c.updateTaskScope(s.journal.sink().presented())
	c.frame(t)
	if len(c.navigation.Matches) != 3 || c.callerCounts["/root/child/nested"].Added != 1 || c.callerCounts["/root/sibling"].Added != 1 {
		t.Fatalf("late native ancestry did not join saved captures: matches=%v callers=%v", c.navigation.Matches, c.callerCounts)
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-diff-mounted-children.txt", strings.Join(c.rendering.Lines, "\n"))
	c.cycleTaskScope()
	c.frame(t)
	if len(c.navigation.Matches) != 4 {
		t.Fatal("slice omitted the separately mounted sibling")
	}
	reader, err := openMekugiReplayStore(s.owner.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err := reader.liveDiffSnapshot(t.Context(), c.scope)
	if err != nil {
		t.Fatal(err)
	}
	c.data = data
	c.view.Merge(data.files())
	c.taskScope = liveDiffSubslice
	c.updateTaskScope(s.journal.sink().presented())
	c.frame(t)
	if len(c.navigation.Matches) != 3 {
		t.Fatal("restart lost mounted task attribution")
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-diff-mounted-children.txt", strings.Join(c.rendering.Lines, "\n"))
	for _, call := range calls {
		history := nativeObservationHistory(t, reader, call, "after")
		want := "/root"
		if call.Binding.Agent != "" {
			want = "native/" + call.Binding.Agent
		}
		if history.Caller != want {
			t.Fatal("presentation rewrote retained native caller evidence")
		}
		record, found, err := reader.read(root.Workspace, observationKey(call)+"/after", false)
		task := "/1"
		if call.Binding.Agent == "" {
			task = "/1/1"
		}
		if err != nil || !found || record.TaskPath != task {
			t.Fatalf("ancestry refresh reassigned the retained task: %q %v", record.TaskPath, err)
		}
	}
	isolated, err := reader.liveDiffSnapshot(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{root.Workspace: {observationThread(root): true}}})
	if err != nil || len(isolated.files()) != 1 {
		t.Fatalf("root-only restart admitted child captures: %v %v", isolated, err)
	}
}

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
