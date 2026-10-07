package router

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotJournalOutcomesDoNotConsumeSlices(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	apply := func(thread string, mutations ...journalMutation) []string {
		t.Helper()
		paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", mutations)
		if err != nil {
			t.Fatal(err)
		}
		return paths
	}
	thread := transform.shellThreadID
	apply(thread, journalMutation{Op: "add", Kind: "task", Title: new("First slice"), State: new("done")})
	for _, id := range []string{"first-outcome", "second-outcome"} {
		response := regressionFinalResponse(t, id, "Slice answer.")
		if err := transform.captureNaturalJournalAnswer(response); err != nil {
			t.Fatal(err)
		}
		if err := transform.captureNaturalJournalAnswer(response); err != nil {
			t.Fatal(err)
		}
	}
	proxy.journals = newJournalStore()
	if got := apply(thread, journalMutation{Op: "add", Kind: "task", Title: new("Second slice")}); !reflect.DeepEqual(got, []string{"/2"}) {
		t.Fatalf("Outcomes consumed slice ordinals after restart: %v", got)
	}
	journal, exists, err := readThreadJournal(proxy.replayStore, workspace, thread)
	if err != nil || !exists || len(journal.Items) != 4 || journal.Items[1].Path != "/outcome-amber" || journal.Items[2].Path != "/outcome-apple" {
		t.Fatalf("retained Outcome identity/replay: %+v, %v", journal.Items, err)
	}
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", thread); err != nil {
		t.Fatal(err)
	}
	if got := apply("fork", journalMutation{Op: "add", Text: new("Fork answer."), Answer: new(true)}, journalMutation{Op: "add", Kind: "task", Title: new("Fork slice")}); got[1] != "/3" {
		t.Fatalf("fork Outcome consumed a slice ordinal: %v", got)
	}
	if got := apply(thread, journalMutation{Op: "add", Kind: "task", Title: new("Source slice")}); !reflect.DeepEqual(got, []string{"/3"}) {
		t.Fatalf("fork changed source numbering: %v", got)
	}
	// Fix timestamps before invoking the actual pane renderer.
	for i := range journal.Items {
		journal.Items[i].CreatedAt, journal.Items[i].UpdatedAt = "", ""
	}
	assertNativeJournalSnapshot(t, "journal-outcome-slice-paths", new(nativeJournalView).render(&journal, 80, 8, false, false, livediff.DarkTheme))
}

func TestJournalOutcomePathsPreserveRetainedReferences(t *testing.T) {
	j := threadJournal{Version: 2, NextOrdinal: map[string]uint64{"": 2}, Items: []journalItem{
		{ID: "/1", Path: "/1", Kind: "task", Title: "Existing slice", State: "done"},
		{ID: "amber", Path: "/2", Kind: "answer", Title: "Outcome", TerminalOnly: true, Text: "Existing answer."},
		{ID: "apple", TerminalOnly: true, Text: "New answer."},
	}}
	j.ensureTree()
	if j.Items[1].Path != "/2" || j.Items[2].Path != "/outcome-apple" {
		t.Fatalf("retained references changed: %+v", j.Items)
	}
	paths, err := j.applyTree(journalMutation{Op: "add", Kind: "task", Title: new("Next slice")})
	if err != nil || !reflect.DeepEqual(paths, []string{"/3"}) {
		t.Fatalf("retained ordinals changed or new Outcome consumed one: %v, %v", paths, err)
	}
}

func TestJournalV1OutcomesDoNotConsumeOrdinals(t *testing.T) {
	j := threadJournal{Version: 1, Items: []journalItem{
		{ID: "amber", TerminalOnly: true, Text: "Old answer.", Created: 1, Updated: 1},
		{ID: "apple", Text: "Old note.", Created: 2, Updated: 2},
	}}
	j.ensureTree()
	if j.Items[0].Path != "/outcome-amber" || j.Items[1].Path != "/1" || len(j.Events) != 2 || j.Events[0].Path != j.Items[0].Path {
		t.Fatalf("v1 Outcome consumed an ordinal or lost its event: %+v", j)
	}
}
