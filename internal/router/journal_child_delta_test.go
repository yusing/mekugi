package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"github.com/yusing/mekugi"
	"strings"
	"testing"
)

func TestChildJournalResultDeltaAcknowledgement(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, acknowledge := range []bool{false, true} {
			name := "json"
			if stream {
				name = "sse"
			}
			if !acknowledge {
				name += "/retry"
			} else {
				name += "/delivered"
			}
			t.Run(name, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				var err error
				proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				first, _ := prepareActivityTest(t, proxy, "first", "child", "root", "/root/child", nil)
				workspace := first.directory
				applyChildDeltaItems(t, proxy, workspace,
					journalMutation{Op: "add", Text: new("Earlier result")},
					journalMutation{Op: "add", Text: new("Unchanged prior note")})
				firstResult := childDeltaResult(t, first, stream, acknowledge, "first-result")
				if !strings.Contains(firstResult, "Earlier result") {
					t.Fatalf("first result omitted its note: %q", firstResult)
				}
				first.Close()

				// A new router process must recover the delivery cursor from the store.
				proxy.journals = newJournalStore()
				proxy.activity = newSubagentActivity()
				proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
				applyChildDeltaItems(t, proxy, workspace,
					journalMutation{Op: "edit", ID: "amber", Text: new("Revised earlier result")},
					journalMutation{Op: "add", Text: new("Follow-up finding")})
				followup, _ := prepareActivityTest(t, proxy, "followup", "child", "root", "/root/child", nil)
				defer followup.Close()
				result := childDeltaResult(t, followup, stream, true, "followup-result")
				if !strings.Contains(result, "Follow-up finding") {
					t.Fatalf("follow-up result lost new note: %q", result)
				}
				if !strings.Contains(result, "Revised earlier result") || strings.Contains(result, "Earlier result\n") {
					t.Fatalf("follow-up lost revision or repeated stale body: %q", result)
				}
				if strings.Contains(result, "Unchanged prior note") != !acknowledge {
					t.Fatalf("incorrect acknowledged window (first delivered=%v): %q", acknowledge, result)
				}
				journal, exists, err := readThreadJournal(proxy.replayStore, workspace, "child")
				if err != nil || !exists || journal.ResultSeq == 0 {
					t.Fatalf("result cursor not persisted after delivery: %+v, exists=%v, err=%v", journal, exists, err)
				}
			})
		}
	}
}

func applyChildDeltaItems(t *testing.T, proxy *mekugiProxy, workspace string, mutations ...journalMutation) {
	t.Helper()
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", mutations); err != nil {
		t.Fatal(err)
	}
}

func childDeltaResult(t *testing.T, child *mekugiResponseTransform, stream, acknowledge bool, responseID string) string {
	t.Helper()
	requestJournalFinish(t, child)
	var result string
	if stream {
		events, err := child.TransformSSE([]byte(`{"type":"response.completed","response":{"id":"` + responseID + `","status":"completed","output":[]}}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if acknowledge {
				child.Delivered(event)
			}
			var envelope struct {
				Type string                       `json:"type"`
				Item map[string]jsonv1.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(event, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Type == "response.output_item.done" && jsonString(envelope.Item, "phase") == "final_answer" {
				result = commentaryMessageText(envelope.Item)
			}
		}
	} else {
		wire, err := child.TransformJSON([]byte(`{"id":"` + responseID + `","status":"completed","output":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if acknowledge {
			child.Delivered(wire)
		}
		var response struct {
			Output []map[string]jsonv1.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(wire, &response); err != nil {
			t.Fatal(err)
		}
		for _, item := range response.Output {
			if jsonString(item, "phase") == "final_answer" {
				result = commentaryMessageText(item)
			}
		}
	}
	child.ReleaseDelivery()
	if result == "" {
		t.Fatal("child result missing")
	}
	return result
}

func TestChildJournalChangesDeltaRecovery(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	var err error
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := prepareActivityTest(t, proxy, "first", "child", "root", "/root/child", nil)
	store := proxy.replayStore.scoped(child.ctx)
	id, err := store.reserveChange(t.Context(), child.directory, "child", "original")
	if err != nil {
		t.Fatal(err)
	}
	put := func(call, path string) {
		t.Helper()
		if err := store.put(t.Context(), child.directory, map[string]mekugiHistory{call: {
			ChangeID: id, CorrelationID: "original", ExecutingThread: "child",
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(path, path, "", "new\n")},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	put("first-call", "first.txt")
	first := childDeltaResult(t, child, false, true, "first")
	if !strings.Contains(first, "first.txt") {
		t.Fatalf("missing first evaluation: %s", first)
	}
	child.Close()
	put("second-call", "second.txt")
	next, _ := prepareActivityTest(t, proxy, "next", "child", "root", "/root/child", nil)
	defer next.Close()
	result := childDeltaResult(t, next, false, true, "second")
	if strings.Contains(result, "first.txt") || !strings.Contains(result, "second.txt") || !strings.Contains(result, id) ||
		!strings.Contains(result, "Cumulative: 2 recorded evaluations across 1 retained changes.") {
		t.Fatalf("incorrect recovery evaluation window: %s", result)
	}
}
