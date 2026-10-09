package router

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func newDurableTreeTransform(t *testing.T) (*mekugiResponseTransform, *mekugiProxy, *parsedResponsesRequest, string) {
	t.Helper()
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	return newMekugiTestTransformWithProxy(t, proxy)
}

func TestJournalNativeTreePlanAndRead(t *testing.T) {
	t.Parallel()
	transform, proxy, _, _ := newDurableTreeTransform(t)
	call := func(id, arguments string) map[string]jsonv1.RawMessage {
		t.Helper()
		result, err := transform.executeJournalCall(map[string]jsonv1.RawMessage{
			"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
			"call_id": mustMarshalJSON(id), "arguments": mustMarshalJSON(arguments),
		})
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]jsonv1.RawMessage
		if err := jsonv2.Unmarshal([]byte(jsonString(result, "output")), &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	plan := call("tree-plan", `{"op":"plan","tasks":[{"title":"Parser","state":"working","tasks":["AST"]},"Renderer"]}`)
	if string(plan["ok"]) != "true" {
		t.Fatalf("native plan failed: %s", mustMarshalJSON(plan))
	}
	read := call("tree-read", `{"op":"read","p":"/1","depth":1}`)
	if string(read["ok"]) != "true" {
		t.Fatalf("native read failed: %s", mustMarshalJSON(read))
	}
	var nodes []journalNode
	if err := jsonv2.Unmarshal(read["items"], &nodes); err != nil {
		t.Fatalf("native read is not Node objects: %s: %v", mustMarshalJSON(read), err)
	}
	if len(nodes) != 1 || nodes[0].Path != "/1" || nodes[0].Kind != "task" || len(nodes[0].Children) != 1 || nodes[0].Children[0].Path != "/1/1" {
		t.Fatalf("subtree/depth not preserved: %+v", nodes)
	}
	journal, exists, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || !exists || len(journal.Events) != 3 {
		t.Fatalf("native plan did not reach durable tree: %+v exists=%v err=%v", journal, exists, err)
	}
	batched := call("tree-batch", `{"op":"read","journal":[{"op":"set","p":"/2","state":"working"},{"op":"add","under":"/2","kind":"note","title":"Evidence"}]}`)
	if string(batched["ok"]) != "true" {
		t.Fatalf("native batched mutation failed: %s", mustMarshalJSON(batched))
	}
	journal, exists, err = readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || !exists || len(journal.Events) != 5 || journal.Items[2].State != "working" {
		t.Fatalf("native batch not atomic/durable: %+v exists=%v err=%v", journal, exists, err)
	}
}

func TestJournalTreeBatchMutationIsAtomic(t *testing.T) {
	t.Parallel()
	transform, proxy, _, _ := newDurableTreeTransform(t)
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
		{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`"Parent"`)}},
		{Op: "set", P: "/1", State: new("done")},
		{Op: "add", Under: "/1", Kind: "task", Title: new("Open child")},
	})
	if err == nil || !strings.Contains(err.Error(), "/1/1") {
		t.Fatalf("invalid cross-operation batch accepted: %v", err)
	}
	journal, exists, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || !exists || len(journal.Items) != 0 || len(journal.Events) != 0 {
		t.Fatalf("batch partially persisted: %+v exists=%v err=%v", journal, exists, err)
	}
	paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`"New parent"`)}}})
	if err != nil || !reflect.DeepEqual(paths, []string{"/1"}) {
		t.Fatalf("rollback consumed first ordinal: %v %v", paths, err)
	}
}

func TestJournalEmptyOutcomeOmitsAnswerButShowsRemaining(t *testing.T) {
	t.Parallel()
	for _, final := range []string{"Done.", "done", "DONE.", "   "} {
		t.Run(strconv.Quote(final), func(t *testing.T) {
			transform, proxy, _, _ := newDurableTreeTransform(t)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Remaining task")},
			}); err != nil {
				t.Fatal(err)
			}
			answer := map[string]any{"type": "message", "id": "raw-empty-outcome", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": final}}}
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "empty-outcome", "status": "completed", "output": []any{answer}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]jsonv1.RawMessage `json:"output"`
			}
			if err := jsonv2.Unmarshal(visible, &response); err != nil {
				t.Fatal(err)
			}
			for _, item := range response.Output {
				if jsonString(item, "id") == "raw-empty-outcome" {
					t.Fatalf("raw empty outcome escaped: %s", visible)
				}
			}
			if len(response.Output) == 0 || !strings.Contains(commentaryMessageText(response.Output[len(response.Output)-1]), "Remaining task") {
				t.Fatalf("turn card omitted remaining task: %s", visible)
			}
			journal, exists, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
			if err != nil || !exists || len(journal.Items) != 1 || journal.Items[0].Kind != "task" {
				t.Fatalf("empty outcome created answer node: %+v exists=%v err=%v", journal.Items, exists, err)
			}
		})
	}
}
