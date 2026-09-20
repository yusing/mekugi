package router

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalMainFlushOnlyOwnJournalAfterRestart(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			var err error
			proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
			workspace := root.directory
			seed := func(thread, text string) {
				t.Helper()
				mutations := bindJournalAnswers([]journalMutation{
					{Op: "add", Text: new(text), Answer: new(true)},
					{Op: "add", Text: new("More " + text), Answer: new(true)},
				}, "Shared assignment?")
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", mutations); err != nil {
					t.Fatal(err)
				}
			}
			seed("root", "Main result")
			// Finish several children, including a nested agent.
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
				if bytes.Contains(wire, []byte("Journal flush")) || !bytes.Contains(wire, []byte("Journal result")) || !bytes.Contains(wire, []byte("Result "+node.author)) {
					t.Fatalf("premature or missing child completion: %s", wire)
				}
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, node.thread)
				if err != nil || len(items) != 2 || items[0].Flushed || items[1].Flushed {
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
			if err != nil || len(messages) != 1 {
				t.Fatalf("main snapshot: %v, %v", messages, err)
			}
			// An abandoned main write must leave its revisions pending.
			root.ReleaseDelivery()
			messages, err = root.prepareJournalDelivery(true)
			if err != nil || len(messages) != 1 {
				t.Fatalf("retry lost revisions: %v, %v", messages, err)
			}
			for i, want := range []string{"Main result"} {
				if !strings.Contains(commentaryMessageText(messages[i]), want) {
					t.Fatalf("main flush: %s", mustMarshalJSON(messages))
				}
				body := commentaryMessageText(messages[i])
				if strings.Count(body, "Shared assignment?") != 1 ||
					strings.Count(body, "**Answers:**") != 1 ||
					!strings.Contains(body, "\n- `apple`\n\n  More "+want) {
					t.Fatalf("question context crossed author journals after restart: %s", body)
				}
				root.Delivered(assistantCommentaryDoneEvent(messages[i]))
			}
			root.ReleaseDelivery()
			for _, thread := range []string{"root", "a", "b", "nested"} {
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, thread)
				if err != nil || len(items) != 2 || items[0].Flushed != (thread == "root") || items[1].Flushed != (thread == "root") {
					t.Fatalf("incorrect acknowledgement for %s: %+v, %v", thread, items, err)
				}
			}
			messages, err = root.prepareJournalDelivery(true)
			root.ReleaseDelivery()
			if err != nil || len(messages) != 0 {
				t.Fatalf("repeated main flush: %s, %v", mustMarshalJSON(messages), err)
			}
			requestJournalFinish(t, root)
			// Child edits stay separate from the next main terminal projection.
			seed("a", "Later child revision")
			seed("root", "Later main revision")
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
			if !bytes.Contains(output, []byte("Shared assignment?")) ||
				!bytes.Contains(output, []byte("**Answers:**")) ||
				!bytes.Contains(output, []byte("`arch`")) ||
				!bytes.Contains(output, []byte("`ash`")) ||
				bytes.Contains(output, []byte("`amber`")) {
				t.Fatalf("later delivery did not group only its own answers: %s", output)
			}
			if !bytes.Contains(output, []byte("Later main revision")) || bytes.Contains(output, []byte("Later child revision")) || bytes.Contains(output, []byte("Unrelated result")) {
				t.Fatalf("terminal projection: %s", output)
			}
		})
	}
}

func TestJournalMainFlushRejectsUnprovenTrees(t *testing.T) {
	t.Parallel()
	for _, durable := range []bool{false, true} {
		for _, scenario := range []string{"conflict", "unknown-parent", "cycle", "workspace", "fork"} {
			t.Run(map[bool]string{false: "memory/", true: "disk/"}[durable]+scenario, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
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
			proxy := newManagedMekugiProxy(t)
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
			if err != nil {
				t.Fatalf("record error escaped its tree: %v", err)
			}
		})
	}
}

func TestJournalMainFlushIgnoresOversizedChild(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "main", "root", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	defer child.Close()
	if _, err := proxy.journals.apply(t.Context(), nil, root.directory, "root", "", []journalMutation{{Op: "add", Text: new("Main result")}}); err != nil {
		t.Fatal(err)
	}
	key := journalKey(root.directory, "child")
	journal := proxy.journals.memory[key]
	journal.Items = []journalItem{{ID: "amber", Text: strings.Repeat("x", maxJournalFlushBytes), Updated: 1}}
	proxy.journals.memory[key] = journal
	messages, err := root.prepareJournalDelivery(true)
	defer root.ReleaseDelivery()
	if err != nil || len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Main result") {
		t.Fatalf("child blocked main delivery: messages=%d err=%v", len(messages), err)
	}
}
