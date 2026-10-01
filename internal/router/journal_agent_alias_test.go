package router

import (
	"reflect"
	"testing"
)

func TestJournalAgentAliasesDurableReads(t *testing.T) {
	t.Parallel()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	store := newJournalStore()
	for _, node := range []struct{ thread, parent, author string }{
		{"root", "", "/root"},
		{"a", "root", "/root/a"},
		{"nested", "a", "/root/a/nested"},
		{"b", "root", "/root/b"},
		{"other", "", "/root"},
		{"foreign", "other", "/root/foreign"},
		{"conflicted", "root", "/root/conflicted"},
	} {
		if err := store.initialize(t.Context(), replay, workspace, node.thread, node.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(t.Context(), replay, workspace, node.thread, node.parent, node.author, true); err != nil {
			t.Fatal(err)
		}
		if _, err := store.apply(t.Context(), replay, workspace, node.thread, "", []journalMutation{{Op: "add", Kind: "note", Title: new("Result " + node.thread)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.bindIdentity(t.Context(), replay, workspace, "conflicted", "other", "/root/conflicted", true); err != nil {
		t.Fatal(err)
	}
	for _, restart := range []bool{false, true} {
		if restart {
			replay, err = openMekugiReplayStore(replay.directory)
			if err != nil {
				t.Fatal(err)
			}
			store = newJournalStore()
		}
		for _, query := range []struct{ caller, alias, thread string }{
			{"root", "a", "a"},
			{"root", "a/nested", "nested"},
			{"a", "a/nested", "nested"},
			{"nested", "a", "a"},
		} {
			canonical := "/root/" + query.alias
			want, err := store.readTree(t.Context(), replay, workspace, query.caller, canonical, "", nil, "own")
			if err != nil || len(want) != 1 || want[0].Title != "Result "+query.thread {
				t.Fatalf("canonical read restart=%v query=%+v: %v %v", restart, query, want, err)
			}
			got, err := store.readTree(t.Context(), replay, workspace, query.caller, query.alias, "", nil, "own")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("alias read restart=%v query=%+v: %v %v, want %v", restart, query, got, err, want)
			}
			wantItems, err := store.listAgent(t.Context(), replay, workspace, query.caller, canonical)
			if err != nil {
				t.Fatal(err)
			}
			gotItems, err := store.listAgent(t.Context(), replay, workspace, query.caller, query.alias)
			if err != nil || !reflect.DeepEqual(gotItems, wantItems) {
				t.Fatalf("alias list restart=%v query=%+v: %v %v, want %v", restart, query, gotItems, err, wantItems)
			}
		}
		for _, query := range []struct{ caller, alias string }{
			{"a", "b"}, {"root", "foreign"}, {"other", "a"},
			{"root", "conflicted"}, {"conflicted", "a"},
			{"root", "missing"}, {"root", "a/absent"}, {"missing", "a"},
		} {
			for _, agent := range []string{query.alias, "/root/" + query.alias} {
				if nodes, err := store.readTree(t.Context(), replay, workspace, query.caller, agent, "", nil, "own"); err == nil || err.Error() == "" || len(nodes) != 0 {
					t.Fatalf("unauthorized read restart=%v caller=%q agent=%q: %v %v", restart, query.caller, agent, nodes, err)
				}
				if items, err := store.listAgent(t.Context(), replay, workspace, query.caller, agent); err == nil || err.Error() == "" || len(items) != 0 {
					t.Fatalf("unauthorized list restart=%v caller=%q agent=%q: %v %v", restart, query.caller, agent, items, err)
				}
			}
		}
	}
	// Read aliases do not turn a relative mutation binding into a canonical child.
	if _, err := store.apply(t.Context(), replay, workspace, "root", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Relative mount"), Agent: "a"}}); err == nil {
		t.Fatal("relative mutation binding was accepted")
	}
}
