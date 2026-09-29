package router

import (
	"encoding/json/jsontext"
	"reflect"
	"strings"
	"testing"
)

func treeTestJournal(t *testing.T) (*mekugiProxy, string) {
	t.Helper()
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _ := prepareActivityTest(t, proxy, "tree", "tree", "", "/root", nil)
	return proxy, transform.directory
}

func treeApply(t *testing.T, proxy *mekugiProxy, workspace string, mutations ...journalMutation) []string {
	t.Helper()
	paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", mutations)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func treeSnapshot(t *testing.T, proxy *mekugiProxy, workspace string) threadJournal {
	t.Helper()
	journal, exists, err := readThreadJournal(proxy.replayStore, workspace, "tree")
	if err != nil || !exists {
		t.Fatalf("journal unavailable: exists=%v err=%v", exists, err)
	}
	return journal
}

func TestJournalTreePlanAndStablePaths(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	paths := treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Parser","state":"working","tasks":["Tokenizer","AST"]}`),
		jsontext.Value(`"Renderer"`),
	}})
	if !reflect.DeepEqual(paths, []string{"/1", "/1/1", "/1/2", "/2"}) {
		t.Fatalf("nested plan paths: %v", paths)
	}
	journal := treeSnapshot(t, proxy, workspace)
	if journal.Version != 2 || len(journal.Events) != 4 || journal.NextOrdinal["/1"] != 2 {
		t.Fatalf("tree not durably materialized: version=%d events=%d ordinals=%v", journal.Version, len(journal.Events), journal.NextOrdinal)
	}
	tree, err := journalTree(journal.Items, "", nil)
	if err != nil || len(tree) != 2 || len(tree[0].Children) != 2 || tree[0].State != "working" || tree[1].State != "pending" {
		t.Fatalf("derived nested tree: %+v, %v", tree, err)
	}
	depth := 0
	shallow, err := journalTree(journal.Items, "/1", &depth)
	if err != nil || len(shallow) != 1 || len(shallow[0].Children) != 0 {
		t.Fatalf("depth-limited subtree: %+v, %v", shallow, err)
	}
	if got := treeApply(t, proxy, workspace, journalMutation{Op: "remove", P: "/1/1"}, journalMutation{Op: "add", Under: "/1", Kind: "task", Title: new("Replacement")}); !reflect.DeepEqual(got[len(got)-1:], []string{"/1/3"}) {
		t.Fatalf("deleted ordinal was reused: %v", got)
	}
}

func TestJournalTreeAtomicValidationAndReopen(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Parent","state":"working","tasks":["Child"]}`),
	}})
	before := treeSnapshot(t, proxy, workspace)
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{
		{Op: "set", P: "/1", State: new("done")},
		{Op: "add", Kind: "task", Title: new("Uncommitted")},
	})
	if err == nil || !strings.Contains(err.Error(), "/1/1") {
		t.Fatalf("done parent with open child accepted or missing open path: %v", err)
	}
	after := treeSnapshot(t, proxy, workspace)
	if !reflect.DeepEqual(before.Items, after.Items) || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.NextOrdinal, after.NextOrdinal) {
		t.Fatal("failed batch changed durable tree or consumed ordinal")
	}
	if got := treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Committed")}); !reflect.DeepEqual(got, []string{"/2"}) {
		t.Fatalf("failed batch consumed ordinal: %v", got)
	}
	treeApply(t, proxy, workspace,
		journalMutation{Op: "set", P: "/1", State: new("done")},
		journalMutation{Op: "set", P: "/1/1", State: new("done")})
	journal := treeSnapshot(t, proxy, workspace)
	if journal.Items[0].State != "done" || journal.Items[0].Finished == nil {
		t.Fatalf("end-of-batch completion missing: %+v", journal.Items[0])
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("working")})
	journal = treeSnapshot(t, proxy, workspace)
	if journal.Items[0].State != "working" || journal.Items[0].Finished != nil {
		t.Fatalf("reopen did not preserve current state: %+v", journal.Items[0])
	}
}

func TestJournalTreePlanDropsOnlyPendingAndValidatesReason(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`"Pending"`), jsontext.Value(`{"title":"Working","state":"working"}`), jsontext.Value(`{"title":"Done","state":"done"}`),
	}})
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`"New"`)}})
	journal := treeSnapshot(t, proxy, workspace)
	states := map[string]string{}
	for _, item := range journal.Items {
		states[item.Path] = item.State
	}
	if states["/1"] != "dropped" || states["/2"] != "working" || states["/3"] != "done" || states["/4"] != "pending" || journal.Items[0].Reason == "" {
		t.Fatalf("plan omission changed historical tasks incorrectly: %v", states)
	}
	for _, mutation := range []journalMutation{
		{Op: "set", P: "/4", State: new("blocked")},
		{Op: "add", Kind: "note", Title: new("Fact"), State: new("pending")},
		{Op: "add", Kind: "context", Title: new("Constraint"), State: new("done")},
	} {
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{mutation}); err == nil {
			t.Fatalf("invalid state accepted: %+v", mutation)
		}
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/4", State: new("blocked"), Reason: new("Needs decision")})
}

func TestJournalTreeLogPlacementAndAmbiguity(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`{"title":"One","state":"working"}`)}})
	if got := treeApply(t, proxy, workspace, journalMutation{Op: "log", Text: new("Passed 12 tests")}); !reflect.DeepEqual(got, []string{"/1/1"}) {
		t.Fatalf("single working leaf did not own log: %v", got)
	}
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Two"), State: new("working")})
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "log", Text: new("Ambiguous")}}); err == nil {
		t.Fatal("ambiguous implicit log placement accepted")
	}
	if got := treeApply(t, proxy, workspace, journalMutation{Op: "log", P: "/2", Text: new("Explicit result")}); !reflect.DeepEqual(got, []string{"/2/1"}) {
		t.Fatalf("explicit log placement: %v", got)
	}
}

func TestJournalTreeForkAndRestartIsolation(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`"Original"`)}})
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", "tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "fork", "", []journalMutation{{Op: "set", P: "/1", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	var err error
	proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	root := treeSnapshot(t, proxy, workspace)
	fork, exists, err := readThreadJournal(proxy.replayStore, workspace, "fork")
	if err != nil || !exists || root.Items[0].State != "pending" || fork.Items[0].State != "working" {
		t.Fatalf("fork mutation leaked or restart lost tree: root=%+v fork=%+v exists=%v err=%v", root.Items, fork.Items, exists, err)
	}
	if !reflect.DeepEqual(root.NextOrdinal, fork.NextOrdinal) || len(root.Events) != 1 || len(fork.Events) != 2 {
		t.Fatalf("fork did not copy ordinal/event history independently: root=%+v fork=%+v", root, fork)
	}
}
