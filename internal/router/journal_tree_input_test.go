package router

import (
	"reflect"
	"strings"
	"testing"
)

func TestJournalTreeLogAcceptsCRLFText(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	if got := treeApply(t, proxy, workspace, journalMutation{Op: "log", Text: new("\r\nPassed 12 tests\r\ngo test ./...\r\n")}); !reflect.DeepEqual(got, []string{"/1"}) {
		t.Fatalf("CRLF log path: %v", got)
	}
	item := treeSnapshot(t, proxy, workspace).Items[0]
	if item.Title != "Passed 12 tests" || item.Body != "go test ./..." {
		t.Fatalf("CRLF log split: title=%q body=%q", item.Title, item.Body)
	}
}

func TestJournalTextAddRejectsTreeFields(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Parent")})
	before := treeSnapshot(t, proxy, workspace)
	for _, mutation := range []journalMutation{
		{Op: "add", Under: "/1", Text: new("Nested finding")},
		{Op: "add", Kind: "task", Text: new("Task")},
		{Op: "add", Text: new("Finding"), Body: new("Detail")},
		{Op: "edit", ID: "amber", P: "/1", Text: new("Edit")},
	} {
		_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{mutation})
		if err == nil || !strings.Contains(err.Error(), "title") {
			t.Fatalf("text mutation silently dropped tree fields: %+v err=%v", mutation, err)
		}
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before.Items, after.Items) {
		t.Fatalf("rejected mutation changed journal: %+v", after.Items)
	}
}
