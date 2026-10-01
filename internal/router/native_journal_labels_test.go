package router

import (
	"encoding/base64"
	"reflect"
	"slices"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func nativeJournalLabelsFixture(t *testing.T) threadJournal {
	t.Helper()
	journals := map[string]threadJournal{
		"main": {Thread: "main", Author: "/root", IdentityKnown: true, Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Review implementation", State: "working", Agent: "/root/review"},
			{Path: "/2", Kind: "task", Title: "Pending assignment", State: "pending", Agent: "/root/docs"},
		}},
		"0199-opaque-review": {Thread: "0199-opaque-review", Parent: "main", Author: "/root/review", IdentityKnown: true, LifecycleState: "working", Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Check lifecycle", State: "done", Author: "/root/review", Body: "Lifecycle evidence remains available."},
			{Path: "/1/1", Kind: "context", Title: "Preserve durable identity", Author: "/root/review"},
			{Path: "/1/2", Kind: "note", Title: "Validated copy and navigation", Author: "/root/review"},
		}},
		"0199-opaque-nested": {Thread: "0199-opaque-nested", Parent: "0199-opaque-review", Author: "/root/review/tests", IdentityKnown: true, Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Inspect regression cases", State: "done", Author: "/root/review/tests"},
		}},
		"0199-opaque-tests": {Thread: "0199-opaque-tests", Parent: "main", Author: "/root/tests", IdentityKnown: true, Items: []journalItem{
			{Path: "/1", Kind: "task", Title: "Run focused suite", State: "done", Author: "/root/tests"},
		}},
	}
	items, err := mountedJournalItems(journals, nil, "main", "main")
	if err != nil {
		t.Fatal(err)
	}
	return threadJournal{Version: 2, TreeAuthored: true, Items: items}
}

func TestUISnapshotNativeJournalMountedLabels(t *testing.T) {
	j := nativeJournalLabelsFixture(t)
	u := nativeJournalSliceUI(t, &j)
	before := slices.Clone(j.Items)
	for _, width := range []int{90, 48} {
		name := "journal-mounted-labels-wide"
		if width == 48 {
			name = "journal-mounted-labels-narrow"
		}
		assertNativeJournalSnapshot(t, name, u.journalView.render(&j, width, 18, false, false, livediff.DarkTheme))
	}
	path := "/1/@0199-opaque-review/1"
	u.shell.openJournalDetail(nativeJournalSliceNode(t, &j, path))
	u.shell.output.layout(80)
	assertNativeJournalDialogSnapshot(t, "journal-mounted-labels-detail", u.shell.output.laid)
	if !reflect.DeepEqual(before, j.Items) {
		t.Fatal("display labels changed durable items")
	}
}

func TestNativeJournalMountedLabelsPreserveTargets(t *testing.T) {
	j := nativeJournalLabelsFixture(t)
	u := nativeJournalSliceUI(t, &j)
	v := &u.journalView
	v.render(&j, 90, 20, false, true, livediff.DarkTheme)
	path := "/1/@0199-opaque-review/1"
	v.selected = slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == path })
	if v.selected < 0 {
		t.Fatal("mounted task is missing")
	}
	if err := u.shell.journalKey("c"); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(path)) + "\a"; u.shell.clipboard != want {
		t.Fatalf("copy lost durable path: %q", u.shell.clipboard)
	}
	if err := u.shell.journalKey(" "); err != nil {
		t.Fatal(err)
	}
	if expanded, exists := v.expanded[path]; !exists || expanded {
		t.Fatal("disclosure did not retain durable path")
	}
	u.shell.openJournalDetail(v.rows[v.selected].node)
	if u.shell.output.pendingSegment != 3 || !u.shell.output.pendingFlash {
		t.Fatal("detail navigation lost original segment identity")
	}
}
