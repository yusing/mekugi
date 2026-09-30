package router

import (
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func nativeJournalFitFixture() threadJournal {
	return threadJournal{Version: 2, TreeAuthored: true, Items: []journalItem{
		{Path: "/1", ID: "/1", Kind: "task", Title: "Recent first", State: "working", Updated: 20},
		{Path: "/1/1", ID: "/1/1", Kind: "note", Title: "Recent evidence", Updated: 20},
		{Path: "/2", ID: "/2", Kind: "task", Title: "Older last", State: "working", Updated: 10},
		{Path: "/2/1", ID: "/2/1", Kind: "note", Title: "Older evidence", Updated: 10},
	}}
}

func nativeJournalFitRow(t *testing.T, v *nativeJournalView, path string) journalPaneRow {
	t.Helper()
	i := slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == path })
	if i < 0 {
		t.Fatalf("missing visible row %s: %+v", path, v.rows)
	}
	return v.rows[i]
}

func TestUISnapshotNativeJournalFitRoomyDefaultExpandsDoneSubtrees(t *testing.T) {
	j := nativeJournalFitFixture()
	j.Items[0].State, j.Items[2].State = "done", "dropped"
	v := new(nativeJournalView)
	frame := v.render(&j, 80, 10, false, false, livediff.DarkTheme)
	if len(v.rows) != 4 || !nativeJournalFitRow(t, v, "/1").open || !nativeJournalFitRow(t, v, "/2").open {
		t.Fatalf("roomy pane did not expand finished subtrees: %+v", v.rows)
	}
	assertNativeJournalSnapshot(t, "journal-fit-roomy", frame)
}

func TestNativeJournalFitOldestFirstNotDisplayOrder(t *testing.T) {
	for _, clock := range []string{"sequence", "timestamp"} {
		t.Run(clock, func(t *testing.T) {
			j := nativeJournalFitFixture()
			if clock == "timestamp" {
				for i := range j.Items {
					// Reverse sequence recency to prove timestamps govern when present.
					stamp := int64(j.Items[i].Updated)
					j.Items[i].UpdatedAt = time.Unix(stamp, 0).UTC().Format(time.RFC3339Nano)
					j.Items[i].Updated = uint64(100 - stamp)
				}
			}
			v := new(nativeJournalView)
			v.render(&j, 80, 3, false, false, livediff.DarkTheme)
			if len(v.rows) != 3 || !nativeJournalFitRow(t, v, "/1").open || nativeJournalFitRow(t, v, "/2").open {
				t.Fatalf("fit did not collapse older last subtree: %+v", v.rows)
			}
			if len(v.expanded) != 0 {
				t.Fatal("automatic fitting persisted a manual disclosure choice")
			}
		})
	}
}

func TestNativeJournalFitNewestDescendantMakesParentRecent(t *testing.T) {
	for _, clock := range []string{"sequence", "timestamp"} {
		t.Run(clock, func(t *testing.T) {
			j := nativeJournalFitFixture()
			j.Items[0].Updated, j.Items[1].Updated = 1, 100
			j.Items[2].Updated, j.Items[3].Updated = 50, 50
			if clock == "timestamp" {
				for i := range j.Items {
					j.Items[i].UpdatedAt = time.Unix(int64(j.Items[i].Updated), 0).UTC().Format(time.RFC3339Nano)
					j.Items[i].Updated = 1
				}
			}
			v := new(nativeJournalView)
			v.render(&j, 80, 3, false, false, livediff.DarkTheme)
			if !nativeJournalFitRow(t, v, "/1").open || nativeJournalFitRow(t, v, "/2").open {
				t.Fatalf("fit ignored fresh descendant of old parent: %+v", v.rows)
			}
		})
	}
}

func TestNativeJournalFitResizeGrowthRestoresAutoCollapsedChildren(t *testing.T) {
	j := nativeJournalFitFixture()
	v := new(nativeJournalView)
	v.render(&j, 80, 3, false, false, livediff.DarkTheme)
	if nativeJournalFitRow(t, v, "/2").open {
		t.Fatal("small pane did not auto-collapse old subtree")
	}
	v.render(&j, 80, 8, false, false, livediff.DarkTheme)
	if len(v.rows) != 4 || !nativeJournalFitRow(t, v, "/2").open {
		t.Fatal("growth retained automatic collapse instead of restoring children")
	}
	v.render(&j, 80, 3, false, false, livediff.DarkTheme)
	if nativeJournalFitRow(t, v, "/2").open {
		t.Fatal("second shrink failed to fit again")
	}
}

func TestNativeJournalFitManualDisclosurePersistsAndProtectsAncestors(t *testing.T) {
	j := nativeJournalFitFixture()
	j.Items = append(j.Items, journalItem{Path: "/2/1/1", ID: "/2/1/1", Kind: "note", Title: "Pinned evidence", Updated: 10})
	j.Items[3].Kind = "task"
	v := new(nativeJournalView)
	v.render(&j, 80, 10, false, false, livediff.DarkTheme)
	v.toggle(nativeJournalFitRow(t, v, "/1"))
	v.toggle(nativeJournalFitRow(t, v, "/2/1"))
	v.render(&j, 80, 10, false, false, livediff.DarkTheme)
	v.toggle(nativeJournalFitRow(t, v, "/2/1"))
	for _, height := range []int{2, 10, 2} {
		v.render(&j, 80, height, false, false, livediff.DarkTheme)
		if nativeJournalFitRow(t, v, "/1").open || !nativeJournalFitRow(t, v, "/2").open || !nativeJournalFitRow(t, v, "/2/1").open {
			t.Fatalf("height %d undid manual choice or collapsed protected ancestor: %+v", height, v.rows)
		}
		nativeJournalFitRow(t, v, "/2/1/1")
		if expanded, explicit := v.expanded["/1"]; !explicit || expanded || !v.expanded["/2/1"] {
			t.Fatalf("manual disclosure choices changed: %v", v.expanded)
		}
	}
}

func TestUISnapshotNativeJournalFitReservesHeaderRowBudget(t *testing.T) {
	j := nativeJournalFitFixture()
	without, with := new(nativeJournalView), new(nativeJournalView)
	without.render(&j, 80, 4, false, false, livediff.DarkTheme)
	frame := with.render(&j, 80, 4, true, false, livediff.DarkTheme)
	if len(without.rows) != 4 || len(with.rows) != 3 || with.height != 3 || with.top != 1 || len(frame) != 4 {
		t.Fatalf("header not deducted from fit budget: without=%d with=%d height=%d top=%d frame=%d", len(without.rows), len(with.rows), with.height, with.top, len(frame))
	}
	if nativeJournalFitRow(t, with, "/2").open {
		t.Fatal("header frame did not fit oldest subtree under state counts")
	}
	assertNativeJournalSnapshot(t, "journal-fit-header-budget", frame)
}

func TestNativeJournalFitSelectionFallsBackToNearestVisibleAncestor(t *testing.T) {
	j := nativeJournalFitFixture()
	j.Items = append(j.Items, journalItem{Path: "/2/1/1", ID: "/2/1/1", Kind: "note", Title: "Selected nested evidence", Updated: 10})
	j.Items[3].Kind = "task"
	v := new(nativeJournalView)
	v.render(&j, 80, 10, false, false, livediff.DarkTheme)
	v.selected = slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == "/2/1/1" })
	v.expanded = map[string]bool{"/2/1": false}
	v.render(&j, 80, 10, false, false, livediff.DarkTheme)
	if v.rows[v.selected].node.Path != "/2/1" {
		t.Fatalf("manual collapse moved selection away from nearest ancestor: %+v", v.rows[v.selected])
	}
	v.expanded = nil
	v.render(&j, 80, 3, false, false, livediff.DarkTheme)
	if v.rows[v.selected].node.Path != "/2" {
		t.Fatalf("automatic collapse did not preserve ancestor selection: %+v", v.rows[v.selected])
	}
}

func TestNativeJournalFitCountsRecentHiddenDescendants(t *testing.T) {
	j := nativeJournalFitFixture()
	j.Items[0].Updated, j.Items[1].Updated = 1, 2
	j.Items[1].Kind = "task"
	j.Items[2].Updated, j.Items[3].Updated = 50, 50
	j.Items = append(j.Items, journalItem{Path: "/1/1/1", ID: "/1/1/1", Kind: "note", Title: "Fresh hidden evidence", Updated: 100})
	v := &nativeJournalView{expanded: map[string]bool{"/1/1": false}}
	v.render(&j, 80, 3, false, false, livediff.DarkTheme)
	if !nativeJournalFitRow(t, v, "/1").open || nativeJournalFitRow(t, v, "/2").open {
		t.Fatalf("fit ignored recent hidden descendant: %+v", v.rows)
	}
	if nativeJournalFitRow(t, v, "/1/1").open {
		t.Fatal("fit undid manual collapse")
	}
}
