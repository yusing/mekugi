package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
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

func TestJournalTreeReadTransportPagesFlatNodes(t *testing.T) {
	t.Parallel()
	items := []journalItem{{Path: "/1", ID: "/1", Kind: "task", Title: "Parent", State: "working", Author: "/root"}}
	for i := 1; i <= journalListPageItems+1; i++ {
		path := "/1/" + strconv.Itoa(i)
		items = append(items, journalItem{Path: path, ID: path, Kind: "note", Title: "Finding", Author: "/root"})
	}
	broker := newCommentaryBroker()
	broker.journalLister = func(_ context.Context, session, thread, agent string) ([]journalItem, error) {
		if session != "session" || thread != "thread" || agent != "" {
			t.Fatalf("read identity: %q %q %q", session, thread, agent)
		}
		return items, nil
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribe("session", "read-call")
	broker.bindActivity(token, "thread")
	base := []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"read","p":"/1","depth":1}`)}
	var all []journalNode
	var revision string
	for offset := 0; ; {
		args := append([]string{}, base...)
		if offset > 0 {
			args = append(args, strconv.Itoa(offset), revision)
		}
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, args)
		if !matched || err != nil {
			t.Fatalf("read page %d: matched=%v err=%v", offset, matched, err)
		}
		var page struct {
			Items    []journalNode `json:"items"`
			Next     *int          `json:"next"`
			Revision string        `json:"revision"`
		}
		if err := jsonv2.Unmarshal(output.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if revision != "" && revision != page.Revision {
			t.Fatal("paged read revision changed")
		}
		revision = page.Revision
		all = append(all, page.Items...)
		if page.Next == nil {
			break
		}
		offset = *page.Next
	}
	if len(all) != len(items) || all[0].Path != "/1" || len(all[0].Children) != 0 || all[len(all)-1].Path != items[len(items)-1].Path {
		t.Fatalf("read pages are not flat ordered nodes: %+v", all)
	}
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

func TestJournalCodeModeTreeReadReassemblesPages(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute lowered Code Mode")
	}
	transform, _ := newRuntimeCommentaryTransform(t)
	lowered, changed, err := transform.lowerCodeModeCommentary("read-call", `const nodes = await journal({op:"read",p:"/1",depth:1,view:"tasks"}); process.stdout.write(JSON.stringify(nodes));`)
	if err != nil || !changed {
		t.Fatalf("lowering: changed=%t err=%v", changed, err)
	}
	first := `{"ok":true,"items":[{"path":"/1","kind":"task","title":"Parent","state":"working","children":[]}],"next":1,"revision":"` + strings.Repeat("a", 64) + `"}`
	last := `{"ok":true,"items":[{"path":"/1/1","kind":"task","title":"Child","state":"pending","children":[]}]}`
	script := `let calls=0; const tools={exec_command:async ({cmd})=>{
  if (!decodeURIComponent(cmd).includes('"view":"tasks"')) throw new Error("read view lost");
  calls++;
  if(calls===1) return {exit_code:0,output:` + strconv.Quote(first) + `};
  if(calls!==2 || !cmd.endsWith(" 1 "+"a".repeat(64))) throw new Error("invalid continuation command");
  return {exit_code:0,output:` + strconv.Quote(last) + `};
}}; (async()=>{` + lowered + `})().catch(error=>{console.error(error);process.exitCode=1;});`
	output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("lowered read: %s: %v", output, err)
	}
	var nodes []journalNode
	if err := jsonv2.Unmarshal(output, &nodes); err != nil || len(nodes) != 1 || len(nodes[0].Children) != 1 || nodes[0].Children[0].Path != "/1/1" {
		t.Fatalf("paged read did not rebuild children: %s: %v", output, err)
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

func TestJournalTreeStockFieldStripsAndCommitsBatch(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	transform.commentaryTools = commentaryToolCatalog{
		functionToolKey("functions", "exec_command"): {qualifiedName: "functions.exec_command"},
	}
	item := map[string]jsonv1.RawMessage{
		"type": mustMarshalJSON("function_call"), "namespace": mustMarshalJSON("functions"),
		"name": mustMarshalJSON("exec_command"), "call_id": mustMarshalJSON("tree-stock-batch"),
		"arguments": mustMarshalJSON(`{"cmd":"true","journal":[{"op":"plan","tasks":["Task"]},{"op":"set","p":"/1","state":"working"},{"op":"add","under":"/1","kind":"note","title":"Evidence"}]}`),
	}
	if _, err := transform.transformStructuredCommentary(item); err != nil {
		t.Fatal(err)
	}
	if jsonString(item, "arguments") != `{"cmd":"true"}` {
		t.Fatalf("stock tool received journal field: %s", item["arguments"])
	}
	journal, exists, err := readThreadJournal(proxy.replayStore, workspace, transform.shellThreadID)
	if err != nil || !exists || len(journal.Events) != 3 || len(journal.Items) != 2 || journal.Items[0].State != "working" {
		t.Fatalf("stock carrier failed to commit one tree batch: %+v exists=%v err=%v", journal, exists, err)
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
