package router

import (
	"encoding/json/jsontext"
	"reflect"
	"strings"
	"testing"
)

func TestJournalLogResolvesUnderSpecifiedTargets(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Parser","state":"working","tasks":[{"title":"Tokenizer","state":"working"},{"title":"AST","state":"working"}]}`),
		jsontext.Value(`{"title":"Renderer","state":"working"}`),
	}})
	for _, test := range []struct {
		name  string
		setup []journalMutation
		p     string
		want  string
	}{
		// /1/1, /1/2 and /2 are working leaves without a common task.
		{name: "unrelated working leaves", want: "/3"},
		{name: "sibling working leaves", setup: []journalMutation{{Op: "set", P: "/2", State: new("done")}}, want: "/1/3"},
		{name: "note path", p: "/1/3", want: "/1/4"},
		{name: "root note path", p: "/3", want: "/4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.setup) != 0 {
				treeApply(t, proxy, workspace, test.setup...)
			}
			got := treeApply(t, proxy, workspace, journalMutation{Op: "log", P: test.p, Text: new("Established fact")})
			if !reflect.DeepEqual(got, []string{test.want}) {
				t.Fatalf("log path = %v, want %s", got, test.want)
			}
		})
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{{Op: "log", P: "/9", Text: new("Lost")}}); err == nil || !strings.Contains(err.Error(), "journal path not found: /9") {
		t.Fatalf("missing log path error = %v", err)
	}
}

func TestJournalRejectionsNameTheCorrection(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	treeApply(t, proxy, workspace,
		journalMutation{Op: "add", Kind: "task", Title: new("Parser")},
		journalMutation{Op: "add", Under: "/1", Title: new("Tokenizer is table driven")},
		journalMutation{Op: "add", Title: new("Root finding")},
	)
	for _, test := range []struct {
		name      string
		mutations []journalMutation
		want      string
	}{
		{name: "nested note parent", mutations: []journalMutation{{Op: "add", Under: "/1/1", Title: new("Detail")}},
			want: `journal parent must be a task: /1/1 is a note ("Tokenizer is table driven"); use its task /1 or omit under for the root`},
		{name: "root note parent", mutations: []journalMutation{{Op: "plan", Under: "/2", Tasks: []jsontext.Value{jsontext.Value(`"Step"`)}}},
			want: `journal parent must be a task: /2 is a note ("Root finding"); omit under for the root`},
		{name: "batch position", mutations: []journalMutation{{Op: "log", Text: new("Fine")}, {Op: "set", P: "/7", State: new("done")}},
			want: "operation 2 (set): journal path not found: /7; recover task paths with read view tasks, or every node's path with read view outline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", test.mutations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
	if journal := treeSnapshot(t, proxy, workspace); len(journal.Items) != 3 {
		t.Fatalf("rejected batch applied: %+v", journal.Items)
	}
}
