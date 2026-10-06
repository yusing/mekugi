package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// A corrected decision stays as history but must stop reading as current.
func supersessionFixture(t *testing.T) (*mekugiResponseTransform, *mekugiProxy, string) {
	t.Helper()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{
		{Op: "add", Kind: "context", Title: new("Separate preview UI"), Body: new("Build a minimal parallel screen first")},
		{Op: "add", Kind: "task", Title: new("Minimal client"), State: new("working")},
		{Op: "log", P: "/2", Text: new("Parallel screen renders streamed replies")},
		{Op: "set", P: "/2", State: new("done")},
		{Op: "add", Kind: "context", Title: new("Same Mekugi UI"), Body: new("Claude is another backend of the existing UI")},
		{Op: "add", Kind: "task", Title: new("Connect the existing UI"), State: new("working")},
	}); err != nil {
		t.Fatal(err)
	}
	return transform, proxy, workspace
}

func readSupersessionNodes(t *testing.T, proxy *mekugiProxy, workspace, thread, view string) map[string]journalNode {
	t.Helper()
	nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, thread, "", "", nil, view)
	if err != nil {
		t.Fatal(err)
	}
	flat := make(map[string]journalNode)
	var walk func([]journalNode)
	walk = func(nodes []journalNode) {
		for _, node := range nodes {
			flat[node.Path] = node
			walk(node.Children)
		}
	}
	walk(nodes)
	return flat
}

func TestJournalSupersessionValidatesAndRemainsReadableHistory(t *testing.T) {
	transform, proxy, workspace := supersessionFixture(t)
	thread := transform.shellThreadID
	apply := func(mutations ...journalMutation) error {
		_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", mutations)
		return err
	}
	if err := apply(journalMutation{Op: "set", P: "/1", SupersededBy: new("/3")}, journalMutation{Op: "set", P: "/2", SupersededBy: new("/4")}); err != nil {
		t.Fatal(err)
	}
	nodes := readSupersessionNodes(t, proxy, workspace, thread, "")
	if nodes["/1"].SupersededBy != "/3" || nodes["/2"].SupersededBy != "/4" || nodes["/1"].Body == "" || nodes["/2/1"].Title == "" {
		t.Fatalf("superseded nodes must keep their content and pointer: %+v", nodes)
	}

	for _, tc := range []struct {
		name      string
		mutations []journalMutation
		want      string
	}{
		{"open task", []journalMutation{{Op: "set", P: "/4", SupersededBy: new("/3")}}, "only a done or dropped task"},
		{"self", []journalMutation{{Op: "set", P: "/3", SupersededBy: new("/3")}}, "outside this subtree"},
		{"descendant", []journalMutation{{Op: "set", P: "/2", SupersededBy: new("/2/1")}}, "outside this subtree"},
		{"missing", []journalMutation{{Op: "set", P: "/3", SupersededBy: new("/9")}}, "does not exist"},
		{"other op", []journalMutation{{Op: "log", P: "/4", Text: new("Fact"), SupersededBy: new("/3")}}, "only supported by set"},
		{"dangling removal", []journalMutation{{Op: "remove", P: "/3"}}, "does not exist"},
		{"reopen superseded", []journalMutation{{Op: "set", P: "/2", State: new("working")}}, "only a done or dropped task"},
		{"open descendant", []journalMutation{
			{Op: "add", Under: "/4", Kind: "task", Title: new("Stale subtask")},
			{Op: "set", P: "/4", State: new("dropped"), Reason: new("Replaced"), SupersededBy: new("/3")},
		}, "superseded subtree has open task /4/1"},
		{"open task under superseded", []journalMutation{{Op: "add", Under: "/2", Kind: "task", Title: new("Late subtask")}}, "superseded subtree has open task"},
		{"cycle", []journalMutation{{Op: "set", P: "/3", SupersededBy: new("/1")}}, "leads back to this node"},
		{"cycle through ancestor", []journalMutation{{Op: "set", P: "/1", SupersededBy: new("/2/1")}, {Op: "set", P: "/2", SupersededBy: new("/1")}}, "leads back to this node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := apply(tc.mutations...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want rejection containing %q", err, tc.want)
			}
			after := readSupersessionNodes(t, proxy, workspace, thread, "")
			if len(after) != len(nodes) || after["/1"].SupersededBy != "/3" || after["/2"].State != "done" {
				t.Fatalf("rejected batch changed the journal: %+v", after)
			}
		})
	}

	// Removing a replacement is valid once the same batch clears or retargets its pointers.
	if err := apply(journalMutation{Op: "remove", P: "/3"}, journalMutation{Op: "set", P: "/1", SupersededBy: new("")}); err != nil {
		t.Fatal(err)
	}
	if after := readSupersessionNodes(t, proxy, workspace, thread, ""); after["/1"].SupersededBy != "" || after["/2"].SupersededBy != "/4" {
		t.Fatalf("cleared pointer not applied: %+v", after)
	}
}

func TestJournalOutlineViewListsEveryOwnKindWithoutBodies(t *testing.T) {
	transform, proxy, workspace := supersessionFixture(t)
	nodes := readSupersessionNodes(t, proxy, workspace, transform.shellThreadID, "outline")
	for path, kind := range map[string]string{"/1": "context", "/2": "task", "/2/1": "note", "/3": "context", "/4": "task"} {
		node, ok := nodes[path]
		if !ok || node.Kind != kind || node.Title == "" || node.Body != "" {
			t.Fatalf("outline %s = %+v, want titled %s without body", path, node, kind)
		}
	}
	if len(nodes) != 5 {
		t.Fatalf("outline read other nodes: %+v", nodes)
	}
}

func TestJournalSummaryCollapsesSupersededNodes(t *testing.T) {
	transform, proxy, workspace := supersessionFixture(t)
	thread := transform.shellThreadID
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "set", P: "/1", SupersededBy: new("/3")},
		{Op: "set", P: "/2", SupersededBy: new("/4")},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, transform.ctx, proxy.replayStore, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\n/1 Separate preview UI; superseded by /3\n", "/3 Same Mekugi UI", "/4 [working] Connect the existing UI"} {
		if !strings.Contains(summary.Text, want) {
			t.Errorf("summary missing %q:\n%s", want, summary.Text)
		}
	}
	for _, stale := range []string{"Build a minimal parallel screen first", "Parallel screen renders streamed replies"} {
		if strings.Contains(summary.Text, stale) {
			t.Errorf("summary presented superseded content %q:\n%s", stale, summary.Text)
		}
	}
}

// Mounted pointers address the same combined namespace as mounted paths, and
// rows display them by the child's local path.
func TestJournalMountedSupersessionUsesCombinedPaths(t *testing.T) {
	journals := map[string]threadJournal{
		"parent": {Thread: "parent", Author: "/root", IdentityKnown: true, Version: 2, TreeAuthored: true, Items: []journalItem{
			{ID: "/1", Path: "/1", Kind: "task", Title: "Delegate", State: "working", Agent: "/root/kid", Author: "/root"},
			{ID: "/2", Path: "/2", Kind: "context", Title: "Parent context", Author: "/root"},
		}},
		"child": {Thread: "child", Parent: "parent", Author: "/root/kid", IdentityKnown: true, Version: 2, TreeAuthored: true, Items: []journalItem{
			{ID: "/1", Path: "/1", Kind: "context", Title: "Old", SupersededBy: "/2", Author: "/root/kid"},
			{ID: "/2", Path: "/2", Kind: "context", Title: "New", Author: "/root/kid"},
		}},
	}
	items, err := mountedJournalItems(journals, nil, "parent", "parent")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Title != "Old" {
			continue
		}
		mount := strings.TrimSuffix(item.Path, "/1")
		if !strings.Contains(mount, "/@") || item.SupersededBy != mount+"/2" {
			t.Fatalf("mounted pointer %q does not address the child's node under %q", item.SupersededBy, mount)
		}
		if _, text := journalNodeParts(livediff.DarkTheme, item.node(), ""); !strings.HasSuffix(ansi.Strip(text), "· superseded by /2") {
			t.Fatalf("mounted row does not show the child-local pointer: %q", ansi.Strip(text))
		}
		return
	}
	t.Fatalf("mounted child node missing: %+v", items)
}

func TestJournalReplayRestoresSupersession(t *testing.T) {
	j := threadJournal{Version: 2, TreeAuthored: true, Author: "/root"}
	item := journalItem{ID: "/1", Path: "/1", Kind: "context", Title: "Old", SupersededBy: "/2", Author: "/root"}
	applyJournalReplayEvent(&j, journalEvent{Seq: 1, Op: "set", Path: "/1", Fields: item.node()})
	if len(j.Items) != 1 || j.Items[0].SupersededBy != "/2" {
		t.Fatalf("replayed item lost superseded_by: %+v", j.Items)
	}
}

func TestUISnapshotNativeJournalSupersededRows(t *testing.T) {
	journal := threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: []journalItem{
		{Path: "/1", ID: "/1", Kind: "context", Title: "Separate preview UI", SupersededBy: "/3", Author: "/root"},
		{Path: "/2", ID: "/2", Kind: "task", Title: "Minimal client", State: "done", SupersededBy: "/4", Author: "/root"},
		{Path: "/2/1", ID: "/2/1", Kind: "note", Title: "Parallel screen renders streamed replies", Author: "/root"},
		{Path: "/3", ID: "/3", Kind: "context", Title: "Same Mekugi UI", Author: "/root"},
		{Path: "/4", ID: "/4", Kind: "task", Title: "Connect the existing UI", State: "working", Author: "/root"},
	}}
	view := new(nativeJournalView)
	assertNativeJournalSnapshot(t, "journal-superseded-wide", view.render(&journal, 80, 8, true, false, livediff.DarkTheme))
	assertNativeJournalSnapshot(t, "journal-superseded-narrow", view.render(&journal, 36, 8, true, false, livediff.DarkTheme))
	// Main and Activity event rows and work-report cards share this renderer.
	var events []string
	for _, row := range []struct {
		item journalItem
		verb string
	}{
		{journalItem{Path: "/1", Kind: "context", Title: "Separate preview UI", SupersededBy: "/3"}, ""},
		{journalItem{Path: "/2", Kind: "task", Title: "Minimal client", State: "dropped", Reason: "Replaced by the existing UI", SupersededBy: "/4"}, "dropped"},
		{journalItem{Path: "/2/1", Kind: "note", Title: "Parallel screen renders streamed replies", SupersededBy: "/4"}, ""},
	} {
		lead, text := journalNodeParts(livediff.DarkTheme, row.item.node(), row.verb)
		events = append(events, lead+text)
	}
	assertNativeJournalSnapshot(t, "journal-superseded-events", events)
}
