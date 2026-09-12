package router

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalMainFlushOrderingAndRestart(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			var err error
			proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
			workspace := root.directory
			seed := func(thread, text string) {
				t.Helper()
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Text: new(text)}}); err != nil {
					t.Fatal(err)
				}
			}
			seed("root", "Main result")
			// Finish in reverse display order, including a nested agent.
			for _, node := range []struct{ thread, parent, author string }{
				{"b", "root", "/root/agent2"},
				{"a", "root", "/root/agent1"},
				{"nested", "a", "/root/agent1/nested"},
			} {
				child, _ := prepareActivityTest(t, proxy, node.thread, node.thread, node.parent, node.author, nil)
				seed(node.thread, "Result "+node.author)
				requestJournalFinish(t, child)
				var wire []byte
				if stream {
					events, err := child.TransformSSE([]byte(`{"type":"response.completed","response":{"id":"child","status":"completed","output":[]}}`))
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						child.Delivered(event)
					}
					wire = bytes.Join(events, nil)
				} else {
					wire, err = child.TransformJSON([]byte(`{"id":"child","status":"completed","output":[]}`))
					if err != nil {
						t.Fatal(err)
					}
					child.Delivered(wire)
				}
				child.ReleaseDelivery()
				child.Close()
				if bytes.Contains(wire, []byte("Journal flush")) || !bytes.Contains(wire, []byte("Journal saved: 1 pending")) {
					t.Fatalf("premature or missing child completion: %s", wire)
				}
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, node.thread)
				if err != nil || len(items) != 1 || items[0].Flushed {
					t.Fatalf("child consumed its journal: %+v, %v", items, err)
				}
			}
			other, _ := prepareActivityTest(t, proxy, "other", "other", "", "/root", nil)
			seed("other", "Unrelated result")
			other.Close()
			root.Close()
			// Lose all live activity and journal caches, preserving only disk records.
			proxy.journals = newJournalStore()
			proxy.activity = newSubagentActivity()
			root, _ = prepareActivityTest(t, proxy, "resumed", "root", "", "/root", nil)
			if messages, err := root.prepareJournalDelivery(false); err != nil || len(messages) != 0 {
				t.Fatalf("nonterminal child flush: %v, %v", messages, err)
			}
			root.ReleaseDelivery()
			root.journalLiveBytes = maxCommentaryPublicationBytes
			root.activityBytes = maxCommentaryPublicationBytes
			messages, err := root.prepareJournalDelivery(true)
			if err != nil || len(messages) != 4 {
				t.Fatalf("tree snapshot: %v, %v", messages, err)
			}
			// An abandoned write must leave all revisions pending.
			root.ReleaseDelivery()
			messages, err = root.prepareJournalDelivery(true)
			if err != nil || len(messages) != 4 {
				t.Fatalf("retry lost revisions: %v, %v", messages, err)
			}
			for i, want := range []string{"Result /root/agent1", "Result /root/agent1/nested", "Result /root/agent2", "Main result"} {
				if !strings.Contains(commentaryMessageText(messages[i]), want) {
					t.Fatalf("flush order: %s", mustMarshalJSON(messages))
				}
				root.Delivered(assistantCommentaryDoneEvent(messages[i]))
			}
			root.ReleaseDelivery()
			for _, thread := range []string{"root", "a", "b", "nested"} {
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, thread)
				if err != nil || len(items) != 1 || !items[0].Flushed {
					t.Fatalf("main did not acknowledge %s: %+v, %v", thread, items, err)
				}
			}
			messages, err = root.prepareJournalDelivery(true)
			root.ReleaseDelivery()
			if err != nil || len(messages) != 0 {
				t.Fatalf("repeated tree flush: %s, %v", mustMarshalJSON(messages), err)
			}
			requestJournalFinish(t, root)
			// Exercise the actual terminal projection after a child edit.
			seed("a", "Later child revision")
			var output []byte
			if stream {
				events, err := root.TransformSSE([]byte(`{"type":"response.completed","response":{"id":"main","status":"completed","output":[]}}`))
				if err != nil {
					t.Fatal(err)
				}
				output = bytes.Join(events, nil)
			} else {
				output, err = root.TransformJSON([]byte(`{"id":"main","status":"completed","output":[]}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			root.ReleaseDelivery()
			if !bytes.Contains(output, []byte("Later child revision")) || bytes.Contains(output, []byte("Unrelated result")) {
				t.Fatalf("terminal projection: %s", output)
			}
		})
	}
}

func TestJournalMainFlushRejectsUnprovenTrees(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, scenario := range []string{"conflict", "unknown-parent", "cycle", "workspace", "fork"} {
			t.Run(map[bool]string{false: "memory/", true: "disk/"}[durable]+scenario, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
				if durable {
					var err error
					proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				}
				root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
				child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
				workspace := root.directory
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "add", Text: new("Must stay private")}}); err != nil {
					t.Fatal(err)
				}
				child.Close()
				switch scenario {
				case "fork":
					if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", "root"); err != nil {
						t.Fatal(err)
					}
					root.Close()
					root, _ = prepareActivityTest(t, proxy, "fork", "fork", "", "/root", nil)
				default:
					if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, "child", func(j *threadJournal, _ bool) error {
						switch scenario {
						case "conflict":
							j.IdentityConflicted = true
						case "unknown-parent":
							j.Parent = "missing"
						case "cycle":
							j.Parent = "child"
						case "workspace":
							// A root in another workspace cannot borrow this tree.
							root.directory = workspace + "/other"
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				if durable {
					proxy.journals = newJournalStore()
					proxy.activity = newSubagentActivity()
				}
				messages, err := root.prepareJournalDelivery(true)
				root.ReleaseDelivery()
				if err != nil || len(messages) != 0 {
					t.Fatalf("unproven tree delivered: %s, %v", mustMarshalJSON(messages), err)
				}
			})
		}
	}
}

func TestJournalMainFlushScopesCorruptRecords(t *testing.T) {
	for _, scenario := range []string{"malformed-unrelated", "invalid-unrelated", "invalid-descendant"} {
		t.Run(scenario, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			var err error
			proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
			workspace := root.directory
			target := "other"
			if scenario == "invalid-descendant" {
				target = "child"
				child, _ := prepareActivityTest(t, proxy, "child", target, "root", "/root/child", nil)
				child.Close()
			} else {
				workspace += "/unrelated"
				if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, target, "/root", ""); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(proxy.replayStore.directory, journalFilename(workspace, target))
			data := []byte("{")
			if scenario != "malformed-unrelated" {
				journal, _, err := readThreadJournal(proxy.replayStore, workspace, target)
				if err != nil {
					t.Fatal(err)
				}
				journal.Receipts = nil
				data = mustMarshalJSON(journal)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = root.prepareJournalDelivery(true)
			root.ReleaseDelivery()
			if (err != nil) != (scenario == "invalid-descendant") {
				t.Fatalf("record error escaped its tree: %v", err)
			}
		})
	}
}

func TestJournalOversizedTreeDoesNotRetainPartialDelivery(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	defer child.Close()
	if _, err := proxy.journals.apply(t.Context(), nil, root.directory, "root", "", []journalMutation{{Op: "add", Text: new("Main result")}}); err != nil {
		t.Fatal(err)
	}
	// Exercise the renderer's capacity guard after a valid earlier tree member.
	key := journalKey(root.directory, "child")
	journal := proxy.journals.memory[key]
	journal.Items = []journalItem{{ID: "j1", Text: strings.Repeat("x", maxJournalFlushBytes), Updated: 1}}
	proxy.journals.memory[key] = journal
	before := len(proxy.memoryCommentary[root.historySessionID])
	messages, err := root.prepareJournalDelivery(true)
	if err == nil || len(messages) != 0 || len(root.journalDeliveries) != 0 ||
		len(proxy.memoryCommentary[root.historySessionID]) != before || root.journalDeliveryRelease != nil {
		t.Fatalf("partial delivery retained: messages=%d deliveries=%d err=%v", len(messages), len(root.journalDeliveries), err)
	}
	journal.Items[0].Text = "Child result"
	proxy.journals.memory[key] = journal
	messages, err = root.prepareJournalDelivery(true)
	defer root.ReleaseDelivery()
	if err != nil || len(messages) != 2 {
		t.Fatalf("retry lost pending tree: messages=%d err=%v", len(messages), err)
	}
}
