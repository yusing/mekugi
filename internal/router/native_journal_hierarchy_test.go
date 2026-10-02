package router

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func nativeJournalHierarchyFixture(t *testing.T) threadJournal {
	t.Helper()
	journals := map[string]threadJournal{
		"main": {Thread: "main", Author: "/root", IdentityKnown: true, Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Finished preparation", State: "done"},
			{Path: "/2", Kind: "answer", Body: "Hierarchy evidence remains available."},
			{Path: "/3", Kind: "task", Title: "Review implementation", State: "working", Agent: "/root/review"},
			{Path: "/3/1", Kind: "task", Title: "Finished local check", State: "done"},
			{Path: "/3/2", Kind: "note", Title: "Local evidence", Body: "Copy preserves the mounted address."},
			{Path: "/3/3", Kind: "context", Title: "Preserve authorization"},
			{Path: "/3/4", Kind: "task", Title: "Pending follow-up", State: "pending"},
			{Path: "/4", Kind: "context", Title: "No new dependencies"},
			{Path: "/5", Kind: "note", Title: "Root evidence"},
		}},
		"opaque-review": {Thread: "opaque-review", Parent: "main", Author: "/root/review", IdentityKnown: true, LifecycleState: "done", Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Inspect lifecycle", State: "done", Author: "/root/review", Body: "Child lifecycle is distinct from its parent's task state."},
			{Path: "/1/1", Kind: "context", Title: "Retain durable identity", Author: "/root/review"},
		}},
		"opaque-tests": {Thread: "opaque-tests", Parent: "main", Author: "/root/tests", IdentityKnown: true, LifecycleState: "working", Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Run focused suite", State: "pending", Author: "/root/tests"},
		}},
		"opaque-unknown": {Thread: "opaque-unknown", Parent: "main", Author: "/root/unstarted", IdentityKnown: true},
	}
	items, err := mountedJournalItems(journals, nil, "main", "main")
	if err != nil {
		t.Fatal(err)
	}
	return threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: items}
}

func TestUISnapshotNativeJournalHierarchy(t *testing.T) {
	j := nativeJournalHierarchyFixture(t)
	before := slices.Clone(j.Items)
	for _, tc := range []struct {
		name     string
		width    int
		expanded map[string]bool
	}{
		{name: "wide", width: 100, expanded: map[string]bool{"/3/@opaque-review": false}},
		{name: "narrow", width: 36, expanded: map[string]bool{"/3/@opaque-review": false}},
		{name: "expanded", width: 100, expanded: map[string]bool{"/3": true, "/3/@opaque-review": true}},
		{name: "collapsed", width: 100, expanded: map[string]bool{"/3": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := nativeJournalView{expanded: tc.expanded}
			assertNativeJournalSnapshot(t, "journal-hierarchy-"+tc.name, v.render(&j, tc.width, 20, false, false, livediff.DarkTheme))
		})
	}
	u := nativeJournalSliceUI(t, &j)
	u.shell.openJournalDetail(nativeJournalSliceNode(t, &j, "/3/@opaque-review/1"))
	u.shell.output.layout(100)
	assertNativeJournalDialogSnapshot(t, "journal-hierarchy-detail", u.shell.output.laid)
	if !reflect.DeepEqual(before, j.Items) {
		t.Fatal("rendering or details changed durable journal items")
	}
}

func TestNativeJournalHierarchySiblingOrdering(t *testing.T) {
	j := nativeJournalHierarchyFixture(t)
	v := new(nativeJournalView)
	v.render(&j, 100, 30, false, false, livediff.DarkTheme)
	for _, tc := range []struct {
		depth int
		under string
		want  []string
	}{
		{depth: 0, want: []string{"/4", "/3", "/5", "/1", "/@agents", "/2"}},
		{depth: 1, under: "/3", want: []string{"/3/3", "/3/2", "/3/4", "/3/1", "/3/@opaque-review"}},
	} {
		var got []string
		for _, row := range v.rows {
			if row.depth == tc.depth && (tc.under == "" || journalParent(row.node.Path) == tc.under) {
				got = append(got, row.node.Path)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("siblings under %q = %v, want %v", tc.under, got, tc.want)
		}
	}
}

func TestNativeJournalHierarchyLifecycleRemainsIndependent(t *testing.T) {
	for _, tc := range []struct {
		parent, lifecycle, label string
	}{
		{parent: "working", lifecycle: "done", label: "finished"},
		{parent: "done", lifecycle: "working", label: "running"},
		{parent: "pending"},
	} {
		t.Run(fmt.Sprintf("parent-%s-agent-%s", tc.parent, tc.lifecycle), func(t *testing.T) {
			items, err := mountedJournalItems(map[string]threadJournal{
				"main":  {Thread: "main", Author: "/root", IdentityKnown: true, Items: []journalItem{{Path: "/1", Kind: "task", Title: "Assignment", State: tc.parent, Agent: "/root/review"}}},
				"child": {Thread: "child", Parent: "main", Author: "/root/review", IdentityKnown: true, LifecycleState: tc.lifecycle},
			}, nil, "main", "main")
			if err != nil {
				t.Fatal(err)
			}
			j := threadJournal{Items: items}
			v := new(nativeJournalView)
			v.render(&j, 100, 5, false, false, livediff.DarkTheme)
			parent := nativeJournalFitRow(t, v, "/1")
			mount := nativeJournalFitRow(t, v, "/1/@child")
			if parent.node.State != tc.parent || mount.node.State != tc.lifecycle {
				t.Fatalf("host lifecycle changed parent state: parent=%q mount=%q", parent.node.State, mount.node.State)
			}
			want := "⎇ review"
			if tc.label != "" {
				want += " · " + tc.label
			}
			if got := strings.TrimSpace(ansi.Strip(v.renderRow(mount, 100, livediff.DarkTheme))); got != "└ "+want {
				t.Errorf("mounted lifecycle row = %q, want %q", got, "└ "+want)
			}
		})
	}
}

func TestNativeJournalHierarchyDescendantCopyAndDetailsKeepIdentity(t *testing.T) {
	j := nativeJournalHierarchyFixture(t)
	u := nativeJournalSliceUI(t, &j)
	v := &u.journalView
	v.render(&j, 100, 30, false, true, livediff.DarkTheme)
	path := "/3/@opaque-review/1"
	v.selected = slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == path })
	if v.selected < 0 {
		t.Fatal("mounted descendant missing")
	}
	if err := u.shell.journalKey("c"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(path)) + "\a"
	if u.shell.clipboard != want {
		t.Fatalf("copy lost mounted address: %q", u.shell.clipboard)
	}
	u.shell.openJournalDetail(v.rows[v.selected].node)
	if !u.shell.output.pendingFlash || u.shell.output.pendingSegment <= 1 {
		t.Fatal("detail navigation did not target the mounted descendant")
	}
	segment := u.shell.output.segments[u.shell.output.pendingSegment-1]
	if segment.Label != "review /1" || !strings.Contains(ansi.Strip(segment.Body), "Child lifecycle is distinct") {
		t.Fatalf("detail navigation lost the descendant's agent-local identity: %+v", segment)
	}
}

func TestNativeJournalHierarchyMountedDescendantsUseLocalPaths(t *testing.T) {
	j := nativeJournalHierarchyFixture(t)
	v := new(nativeJournalView)
	v.render(&j, 100, 30, false, false, livediff.DarkTheme)
	for _, tc := range []struct{ path, local string }{
		{path: "/3/@opaque-review/1", local: "/1"},
		{path: "/3/@opaque-review/1/1", local: "/1/1"},
	} {
		row := nativeJournalFitRow(t, v, tc.path)
		text := ansi.Strip(v.renderRow(row, 100, livediff.DarkTheme))
		if !strings.Contains(text, " "+tc.local+" ") || strings.Contains(text, "review") || strings.Contains(text, "opaque-review") {
			t.Errorf("mounted descendant %s repeated its agent prefix or lost local path: %q", tc.path, text)
		}
	}
}
