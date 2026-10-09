package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJournalTaskRecoveryAfterOutcomeAndRestart(t *testing.T) {
	t.Parallel()
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Completed one","state":"done"}`),
		jsontext.Value(`{"title":"Completed two","state":"done"}`),
		jsontext.Value(`{"title":"Completed three","state":"done"}`),
	}})
	// Router-owned Outcomes do not consume task-tree ordinals.
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Text: new("Previous turn outcome"), Answer: new(true)})
	paths := treeApply(t, proxy, workspace, journalMutation{Op: "plan", Tasks: []jsontext.Value{
		jsontext.Value(`{"title":"Next one","body":"Long task evidence","tasks":["Nested"]}`),
		jsontext.Value(`"Next two"`), jsontext.Value(`"Next three"`),
	}})
	if !reflect.DeepEqual(paths, []string{"/4", "/4/1", "/5", "/6"}) {
		t.Fatalf("plan lost actual path mapping: %v", paths)
	}
	before := treeSnapshot(t, proxy, workspace)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "", []journalMutation{
		{Op: "set", P: "/4", State: new("working")}, {Op: "set", P: "/outcome-amber", State: new("working")},
	}); err == nil {
		t.Fatal("router-owned Outcome was writable")
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected batch changed the journal")
	}
	for _, child := range []string{"child", "sibling"} {
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, child, "", []journalMutation{{Op: "log", Text: new(strings.Repeat("Unrelated child result ", 100))}}); err != nil {
			t.Fatal(err)
		}
	}
	proxy.journals = newJournalStore()
	for _, test := range []struct {
		view          string
		depth         *int
		roots, nested int
	}{
		{"tasks", new(0), 6, 0}, {"tasks", new(2), 6, 1}, {"own", new(0), 7, 0},
	} {
		nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", test.depth, test.view)
		if err != nil || len(nodes) != test.roots {
			t.Fatalf("%s read: %v, %v", test.view, nodes, err)
		}
		if test.view == "tasks" {
			for _, node := range nodes {
				if node.Kind != "task" || node.Body != "" || node.Question != "" || strings.Contains(node.Path, "@") {
					t.Fatalf("noncompact recovery node: %+v", node)
				}
			}
			if len(nodes[3].Children) != test.nested || nodes[3].Path != paths[0] || nodes[4].Path != paths[2] || nodes[5].Path != paths[3] {
				t.Fatalf("recovery paths/depth: %+v", nodes)
			}
		}
	}
	if _, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", ""), "/@agents/@child/1"); !ok {
		t.Fatal("default read no longer mounts agents")
	}
	for _, view := range []string{"tasks", "own"} {
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "child", "/root", "", new(0), view); err != nil {
			t.Fatalf("%s ancestor read rejected: %v", view, err)
		}
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "child", "/root/sibling", "", new(0), view); err == nil {
			t.Fatalf("%s read bypassed ancestry authorization", view)
		}
	}
	// Local recovery must not even need a readable child record.
	child, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil {
		t.Fatal(err)
	}
	child.Receipts = nil // Keep identity provable while content validation fails.
	corrupt, err := json.Marshal(child, json.FormatNilMapAsNull(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "child")), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"own", "tasks"} {
		if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", new(0), view); err != nil {
			t.Fatalf("%s recovery depends on unreadable descendant: %v", view, err)
		}
	}
	if _, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", new(0), "combined"); err == nil {
		t.Fatal("combined read silently lost corrupt mounted evidence")
	}
}
