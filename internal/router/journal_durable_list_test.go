package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalListDurableAncestry(t *testing.T) {
	for _, scenario := range []string{"root restart", "child restart", "sibling", "ambiguous", "retained conflict", "ancestor conflict", "other workspace", "cycle", "missing parent"} {
		t.Run(scenario, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = replay
			root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
			workspace := root.directory
			root.Close()
			for _, node := range []struct{ thread, parent, author string }{
				{"a", "root", "/root/a"},
				{"b", "root", "/root/b"},
				{"nested", "a", "/root/a/nested"},
			} {
				child, _ := prepareActivityTest(t, proxy, node.thread, node.thread, node.parent, node.author, nil)
				child.Close()
			}
			for _, thread := range []string{"root", "a", "b", "nested"} {
				if _, err := proxy.journals.apply(t.Context(), replay, workspace, thread, "", []journalMutation{{Op: "add", Text: new("Result " + thread)}}); err != nil {
					t.Fatal(err)
				}
			}
			caller, parent, author, target, want := "root", "", "/root", "/root/a/nested", "Result nested"
			switch scenario {
			case "child restart":
				caller, parent, author, target, want = "nested", "a", "/root/a/nested", "/root", "Result root"
			case "sibling":
				caller, parent, author, target, want = "a", "root", "/root/a", "/root/b", ""
			case "ambiguous":
				if err := proxy.journals.initialize(t.Context(), replay, workspace, "duplicate", "/root/a/nested", ""); err != nil {
					t.Fatal(err)
				}
				if err := proxy.journals.bindIdentity(t.Context(), replay, workspace, "duplicate", "a", "/root/a/nested", true); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "retained conflict", "ancestor conflict":
				thread, path := "nested", "/root/changed"
				if scenario == "ancestor conflict" {
					thread, path = "a", "/root/changed"
				}
				if err := proxy.journals.bindIdentity(t.Context(), replay, workspace, thread, "root", path, true); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "cycle", "missing parent":
				// Valid records with incomplete or cyclic ancestry cannot prove access.
				if err := proxy.journals.transaction(t.Context(), replay, workspace, "a", func(j *threadJournal, _ bool) error {
					j.Parent = "missing"
					if scenario == "cycle" {
						j.Parent = "nested"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "other workspace":
				want = ""
			}
			// Neither live activity nor the in-memory journal cache survives restart.
			proxy.activity = newSubagentActivity()
			proxy.journals = newJournalStore()
			transform, _ := prepareActivityTest(t, proxy, "resumed", caller, parent, author, nil)
			defer transform.Close()
			if scenario == "other workspace" {
				transform.directory = t.TempDir()
			}
			result, err := transform.executeJournalCall(map[string]json.RawMessage{
				"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, "journal"),
				"call_id":   mustTestJSON(t, "list-relative"),
				"arguments": mustTestJSON(t, fmt.Sprintf(`{"op":"list","agent":%q}`, target)),
			})
			if err != nil {
				t.Fatal(err)
			}
			var output struct {
				OK    bool              `json:"ok"`
				Items []journalListItem `json:"items"`
				Error string            `json:"error"`
			}
			if err := json.Unmarshal([]byte(jsonString(result, "output")), &output); err != nil {
				t.Fatal(err)
			}
			if want == "" {
				if output.OK || output.Error == "" {
					t.Fatalf("unproven access accepted: %+v", output)
				}
			} else if !output.OK || len(output.Items) != 1 || output.Items[0].Text != want {
				t.Fatalf("durable relative list: %+v, want %q", output, want)
			}
		})
	}
}

func TestJournalPreparationSkipsUnchangedWrites(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := newJournalStore()
	const workspace, thread = "/workspace", "root"
	prepare := func(store *journalStore) {
		t.Helper()
		if err := store.initialize(t.Context(), replay, workspace, thread, "/root", ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(t.Context(), replay, workspace, thread, "", "/root", true); err != nil {
			t.Fatal(err)
		}
	}
	prepare(writer)
	path := filepath.Join(replay.directory, journalFilename(workspace, thread))
	stat := func() os.FileInfo {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	before := stat()
	reader := newJournalStore()
	prepare(reader)
	if after := stat(); !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatal("unchanged preparation rewrote durable journal")
	}
	if _, err := writer.apply(t.Context(), replay, workspace, thread, "", []journalMutation{{Op: "add", Text: new("Committed elsewhere")}}); err != nil {
		t.Fatal(err)
	}
	changed := stat()
	if os.SameFile(before, changed) {
		t.Fatal("real mutation did not publish durable journal")
	}
	prepare(reader)
	if after := stat(); !os.SameFile(changed, after) {
		t.Fatal("refresh rewrote durable journal")
	}
	items, err := reader.list(t.Context(), nil, workspace, thread)
	if err != nil || len(items) != 1 || items[0].Text != "Committed elsewhere" {
		t.Fatalf("no-op preparation failed to refresh locked durable state: %+v %v", items, err)
	}
	if err := reader.bindIdentity(t.Context(), replay, workspace, thread, "", "/root/changed", true); err != nil {
		t.Fatal(err)
	}
	conflicted := stat()
	if os.SameFile(changed, conflicted) {
		t.Fatal("identity conflict was not synced")
	}
	prepare(reader)
	if !os.SameFile(conflicted, stat()) {
		t.Fatal("unchanged retained conflict was rewritten")
	}
	journal, _, err := readThreadJournal(replay, workspace, thread)
	if err != nil || !journal.IdentityConflicted {
		t.Fatalf("identity conflict lost: %+v %v", journal, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reader.initialize(t.Context(), replay, workspace, thread, "/root", ""); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("no-op initialization skipped durable validation: %v", err)
	}
}
