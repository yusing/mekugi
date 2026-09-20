package router

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestJournalChildCompletionResult(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		for _, state := range []string{"empty", "pending", "report-now", "reported", "flushed", "edited", "deleted"} {
			t.Run(map[bool]string{false: "json/", true: "sse/"}[stream]+state, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
				defer child.Close()
				apply := func(mutations ...journalMutation) {
					t.Helper()
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, child.directory, "child", "",
						bindJournalAnswers(mutations, "Which result?")); err != nil {
						t.Fatal(err)
					}
				}
				if state != "empty" {
					apply(
						journalMutation{Op: "add", Text: new("First\r\n\r- detail\n\n```go\nok()\n```"), Answer: new(true), ReportNow: state == "report-now"},
						journalMutation{Op: "add", Text: new("Second finding"), Answer: new(true)},
					)
					if state == "reported" || state == "flushed" || state == "edited" {
						items, err := proxy.journals.list(t.Context(), proxy.replayStore, child.directory, "child")
						if err != nil {
							t.Fatal(err)
						}
						if err := proxy.journals.acknowledge(t.Context(), proxy.replayStore, child.directory, "child",
							map[string]uint64{"amber": items[0].Updated, "apple": items[1].Updated}, state != "reported"); err != nil {
							t.Fatal(err)
						}
					}
					if state == "edited" {
						apply(journalMutation{Op: "edit", ID: "amber", Text: new("Revised result")})
					}
					if state == "deleted" {
						apply(journalMutation{Op: "delete", ID: "amber"}, journalMutation{Op: "delete", ID: "apple"})
					}
				}
				nested, _ := prepareActivityTest(t, proxy, "nested", "nested", "child", "/root/child/nested", nil)
				defer nested.Close()
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, child.directory, "nested", "",
					[]journalMutation{{Op: "add", Text: new("Descendant stays separate")}}); err != nil {
					t.Fatal(err)
				}
				before, err := proxy.journals.list(t.Context(), proxy.replayStore, child.directory, "child")
				if err != nil {
					t.Fatal(err)
				}
				requestJournalFinish(t, child)
				var output []map[string]json.RawMessage
				if stream {
					events, err := child.TransformSSE([]byte(`{"type":"response.completed","response":{"id":"child-result","status":"completed","output":[]}}`))
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						child.Delivered(event)
						var envelope struct {
							Type string                     `json:"type"`
							Item map[string]json.RawMessage `json:"item"`
						}
						if err := json.Unmarshal(event, &envelope); err != nil {
							t.Fatal(err)
						}
						if envelope.Type == "response.output_item.done" {
							output = append(output, envelope.Item)
						}
					}
				} else {
					wire, err := child.TransformJSON([]byte(`{"id":"child-result","status":"completed","output":[]}`))
					if err != nil {
						t.Fatal(err)
					}
					child.Delivered(wire)
					var response struct {
						Output []map[string]json.RawMessage `json:"output"`
					}
					if err := json.Unmarshal(wire, &response); err != nil {
						t.Fatal(err)
					}
					output = response.Output
				}
				child.ReleaseDelivery()
				var result string
				for _, item := range output {
					if jsonString(item, "phase") == "final_answer" {
						if result != "" {
							t.Fatal("duplicate completion result")
						}
						result = commentaryMessageText(item)
					}
				}
				if !strings.Contains(result, "Journal result") || strings.Contains(result, "Journal saved:") || strings.Contains(result, "Descendant stays separate") {
					t.Fatalf("missing or incorrectly scoped result: %q", result)
				}
				if len(output) == 0 || jsonString(output[len(output)-1], "phase") != "final_answer" {
					t.Fatal("child completion result must follow pending live notices")
				}
				if state == "report-now" {
					if len(output) != 2 || !strings.HasPrefix(commentaryMessageText(output[0]), "Journal update ") {
						t.Fatalf("missing child live notice before completion: %s", mustMarshalJSON(output))
					}
					before[0].Reported = true
					before[0].EverReported = true
				}
				if len(before) == 0 {
					if !strings.Contains(result, "No journal entries.") || strings.Contains(result, "First") {
						t.Fatalf("empty journal result: %q", result)
					}
				} else {
					for _, want := range []string{"`/root/child`", "`amber`", "**Question:**", "Which result?", "**Answers:**"} {
						if !strings.Contains(result, want) {
							t.Fatalf("result missing %q: %q", want, result)
						}
					}
					if strings.Count(result, "Which result?") != 1 ||
						strings.Count(result, "**Answers:**") != 1 ||
						!strings.Contains(result, "\n- `apple`\n\n  Second finding\n") ||
						strings.Contains(result, "question in `") {
						t.Fatalf("shared assignment and answers are not one block: %s", result)
					}
					if state == "edited" {
						if !strings.Contains(result, "Revised result") || strings.Contains(result, "First") {
							t.Fatalf("stale result: %q", result)
						}
					} else if !strings.Contains(result, "First\n  \n  - detail\n  \n  ```go\n  ok()\n  ```") {
						t.Fatalf("Markdown lost item containment: %q", result)
					}
				}
				after, err := proxy.journals.list(t.Context(), proxy.replayStore, child.directory, "child")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("completion acknowledged or changed journal: before=%+v after=%+v err=%v", before, after, err)
				}
			})
		}
	}
}

func TestJournalChildResultCapacity(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	defer child.Close()
	key := journalKey(child.directory, "child")
	journal := proxy.journals.memory[key]
	// Corrupt in-memory state exercises the renderer guard without changing valid limits.
	journal.Items = []journalItem{{ID: "amber", Text: strings.Repeat("x", maxJournalFlushBytes), Updated: 1}}
	proxy.journals.memory[key] = journal
	requestJournalFinish(t, child)
	if _, err := child.TransformJSON([]byte(`{"id":"oversized","status":"completed","output":[]}`)); err == nil ||
		child.journalDeliveryRelease != nil || len(child.journalDeliveries) != 0 {
		t.Fatalf("oversized child result accepted or retained delivery: %v", err)
	}
}

func TestJournalChildResultAfterRestart(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	var err error
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := prepareActivityTest(t, proxy, "initial", "child", "root", "/root/child", nil)
	workspace := child.directory
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "",
		[]journalMutation{{Op: "add", Text: new("Durable child result")}}); err != nil {
		t.Fatal(err)
	}
	child.Close()
	// Resume with a fresh router store and only the requesting child observed.
	proxy.journals = newJournalStore()
	proxy.activity = newSubagentActivity()
	proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	child, _ = prepareActivityTest(t, proxy, "resumed", "child", "root", "", nil)
	defer child.Close()
	requestJournalFinish(t, child)
	wire, err := child.TransformJSON([]byte(`{"id":"resumed","status":"completed","output":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	child.Delivered(wire)
	child.ReleaseDelivery()
	if !strings.Contains(string(wire), "Durable child result") || !strings.Contains(string(wire), "Journal result `/root/child`") {
		t.Fatalf("resumed completion lost result or identity: %s", wire)
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "child")
	if err != nil || len(items) != 1 || items[0].Reported || items[0].Flushed {
		t.Fatalf("resumed completion consumed journal: %+v, %v", items, err)
	}
}
