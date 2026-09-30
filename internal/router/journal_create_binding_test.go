package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestJournalCreateBindingDurableSingleEventAndReceipt(t *testing.T) {
	proxy, workspace := mountFixture(t)
	batch := []journalMutation{{Op: "add", Kind: "task", Title: new("Delegated"), State: new("working"), Agent: "/root/child"}}
	paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "create-binding", batch)
	if err != nil || !reflect.DeepEqual(paths, []string{"/1"}) {
		t.Fatalf("create and bind: paths=%v err=%v", paths, err)
	}
	before := treeSnapshot(t, proxy, workspace)
	if len(before.Items) != 1 || before.Items[0].Agent != "/root/child" || before.Items[0].State != "working" {
		t.Fatalf("creation omitted task or binding: %+v", before.Items)
	}
	if len(before.Events) != 1 || before.Events[0].Op != "add" || before.Events[0].Path != "/1" || before.Events[0].Fields.Agent != "/root/child" || before.Sequence != 1 {
		t.Fatalf("creation and binding did not form one add event: sequence=%d events=%+v", before.Sequence, before.Events)
	}
	proxy.journals = newJournalStore()
	if mount, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", ""), "/1/@child"); !ok || mount.Agent != "/root/child" {
		t.Fatalf("binding lost after fresh store restart: %+v", mount)
	}
	paths, err = proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "create-binding", batch)
	if err != nil || !reflect.DeepEqual(paths, []string{"/1"}) {
		t.Fatalf("replay receipt: paths=%v err=%v", paths, err)
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("replaying creation receipt changed durable journal")
	}
}

func TestJournalCreateBindingInvalidBatchRollsBack(t *testing.T) {
	cases := []struct {
		name     string
		binding  journalMutation
		existing bool
		prefix   []journalMutation
	}{
		{name: "non-direct child", binding: journalMutation{Agent: "/root/child/grandchild"}},
		{name: "outside parent", binding: journalMutation{Agent: "/other/child"}},
		{name: "ancestor", binding: journalMutation{Agent: "/"}},
		{name: "self", binding: journalMutation{Agent: "/root"}},
		{name: "trailing slash", binding: journalMutation{Agent: "/root/child/"}},
		{name: "empty child", binding: journalMutation{Agent: "/root/"}},
		{name: "note", binding: journalMutation{Kind: "note", Agent: "/root/child"}},
		{name: "context", binding: journalMutation{Kind: "context", Agent: "/root/child"}},
		{name: "default note", binding: journalMutation{Kind: "", Agent: "/root/child"}},
		{name: "duplicate existing", binding: journalMutation{Agent: "/root/child"}, existing: true},
		{name: "duplicate within batch", binding: journalMutation{Agent: "/root/child"}, prefix: []journalMutation{{Op: "add", Kind: "task", Title: new("First mount"), Agent: "/root/child"}}},
		{name: "done unresolved mount", binding: journalMutation{Agent: "/root/missing", State: new("done")}},
		{name: "done open child", binding: journalMutation{Agent: "/root/child", State: new("done")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, workspace := mountFixture(t)
			seed := journalMutation{Op: "add", Kind: "task", Title: new("Existing")}
			if tc.existing {
				seed.Agent = "/root/child"
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "seed", []journalMutation{seed}); err != nil {
				t.Fatal(err)
			}
			before := treeSnapshot(t, proxy, workspace)
			binding := tc.binding
			binding.Op, binding.Title = "add", new("Must not survive")
			// Only the three non-task cases intentionally omit task kind.
			if tc.name != "note" && tc.name != "context" && tc.name != "default note" {
				binding.Kind = "task"
			}
			batch := append([]journalMutation{{Op: "log", Text: new("Valid earlier operation must roll back")}}, tc.prefix...)
			batch = append(batch, binding)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "rejected-binding", batch); err == nil {
				t.Fatal("invalid binding batch accepted")
			}
			if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
				t.Fatal("invalid binding changed durable journal, events, ordinals, sequence, or receipts")
			}
			proxy.journals = newJournalStore()
			if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
				t.Fatal("rolled-back state changed after restart")
			}
			// A failed receipt can be retried with corrected input, without consuming paths.
			paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "rejected-binding", []journalMutation{{Op: "add", Kind: "task", Title: new("Corrected"), Agent: "/root/sibling"}})
			if err != nil || !reflect.DeepEqual(paths, []string{"/2"}) {
				t.Fatalf("failure consumed ordinal or receipt: paths=%v err=%v", paths, err)
			}
		})
	}
}

func TestJournalCreateBindingRetainsSetBinding(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Existing")})
	batch := []journalMutation{{Op: "set", P: "/1", Agent: "/root/child"}}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "set-binding", batch); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, proxy, workspace)
	if before.Items[0].Agent != "/root/child" || len(before.Events) != 2 || before.Events[1].Op != "set" {
		t.Fatalf("set binding no longer works: %+v", before)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "set-binding", batch); err != nil {
		t.Fatal(err)
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("set receipt replay changed journal")
	}
	// Repeating the same binding without a receipt remains valid; changing it does not.
	treeApply(t, proxy, workspace, batch...)
	before = treeSnapshot(t, proxy, workspace)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "invalid-rebind", []journalMutation{
		{Op: "log", Text: new("Must roll back")},
		{Op: "set", P: "/1", Agent: "/root/sibling"},
	}); err == nil {
		t.Fatal("existing binding became mutable")
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("failed rebind changed journal")
	}
}

func TestJournalCreateBindingAuthenticatedCarrier(t *testing.T) {
	proxy, workspace := mountFixture(t)
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	token := proxy.commentary.subscribe(workspace+"\x00session", "create-binding-call")
	proxy.commentary.bindActivity(token, "tree")
	var output bytes.Buffer
	matched, err := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"add","kind":"task","title":"Delegated","state":"working","agent":"/root/child"}`)})
	if !matched || err != nil {
		t.Fatalf("authenticated task creation: matched=%v err=%v output=%s", matched, err, output.String())
	}
	journal := treeSnapshot(t, proxy, workspace)
	if len(journal.Items) != 1 || journal.Items[0].Agent != "/root/child" || len(journal.Events) != 1 || journal.Events[0].Fields.Agent != "/root/child" {
		t.Fatalf("carrier lost create binding: %+v", journal)
	}
	output.Reset()
	matched, err = publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, server.URL, token, url.PathEscape(`{"op":"read","p":"/1"}`)})
	if !matched || err != nil {
		t.Fatalf("authenticated read: matched=%v err=%v output=%s", matched, err, output.String())
	}
	var result struct {
		Items []journalNode `json:"items"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if mount, ok := mountFind(result.Items, "/1/@child"); !ok || mount.Agent != "/root/child" {
		t.Fatalf("carrier read did not expose created mount: %+v", result.Items)
	}
}

func TestJournalCreateBindingRetainedDirectCarrier(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	result, err := transform.executeJournalCall(map[string]jsonv1.RawMessage{
		"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
		"call_id":   mustMarshalJSON("create-binding"),
		"arguments": mustMarshalJSON(`{"op":"add","kind":"task","title":"Delegate","agent":"/root/child"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(jsonString(result, "output")), &output); err != nil || !output.OK || output.ID != "/1" {
		t.Fatalf("retained direct carrier rejected create and bind: %s, err=%v", result["output"], err)
	}
	j, exists, err := readThreadJournal(proxy.replayStore, workspace, "thread-1")
	if err != nil || !exists || len(j.Items) != 1 || j.Items[0].Agent != "/root/child" {
		t.Fatalf("retained direct carrier did not persist binding: %+v, err=%v", j, err)
	}
}
