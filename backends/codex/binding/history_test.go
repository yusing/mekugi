package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func TestDecodeHistoryThreadPreservesWireFields(t *testing.T) {
	raw := []byte(`{"thread":{
		"name":"Saved session","id":"child","parentThreadId":"parent",
		"agentNickname":"Reader","agentRole":"worker","cwd":"/workspace","path":"/rollouts/child.jsonl",
		"source":{"subAgent":{"parentThreadId":"parent","depth":2}},
		"createdAt":1700000001,"updatedAt":1700000009,"historyMode":"full",
		"turns":[{"id":"turn","status":"failed","startedAt":1700000002,"completedAt":1700000008,
			"error":{"message":"Host failure"},"items":[{
				"durationMs":123,"delivery":"async","id":"item","clientId":"submission","type":"commandExecution",
				"questions":[{"id":"question","header":"Choice","question":"Which one?","title":"Pick one",
					"isSecret":true,"options":[{"label":"A","description":"First"}]}],
				"text":"Exact text\n","summary":["Public summary"],"agentThreadId":"agent","agentPath":"/root/reader",
				"kind":"completed","command":"printf 'a  b\\n'","cwd":"/workspace/subdir","source":"agent",
				"processId":"process","path":"relative/file","status":"failed","aggregatedOutput":"Exact output\n",
				"query":"search term","action":{"type":"search","query":"query","queries":["one","two"],"url":"https://example.test","pattern":"needle"},
				"results":[{"opaque":9007199254740993},"verbatim",null],
				"content":[{"type":"text","text":"User content","unknown":9007199254740993}],
				"phase":"final_answer","exitCode":7,
				"commandActions":[{"type":"read","command":"cat file","path":"file","query":"needle"}],
				"changes":[{"path":"old","diff":"@@ -1 +1 @@\n-old\n+new\n","kind":{"type":"update","move_path":"snake","movePath":"camel"}}],
				"tool":"wait","senderThreadId":"parent","receiverThreadIds":["agent"],
				"agentsStates":{"agent":{"status":"completed"}},"prompt":"Assignment","model":"model-id","reasoningEffort":"high"
			}]}]
	}}`)
	change := FileChange{Path: "old", Diff: "@@ -1 +1 @@\n-old\n+new\n"}
	change.Kind.Type, change.Kind.MovePath, change.Kind.MovePath2 = "update", "snake", "camel"
	want := ThreadInfo{
		Name: "Saved session", ID: "child", ParentThreadID: "parent", AgentNickname: "Reader", AgentRole: "worker",
		Cwd: "/workspace", Path: "/rollouts/child.jsonl", Source: jsontext.Value(`{"subAgent":{"parentThreadId":"parent","depth":2}}`),
		CreatedAt: 1700000001, UpdatedAt: 1700000009, HistoryMode: "full",
		Turns: []HistoryTurn{{
			ID: "turn", Status: "failed", StartedAt: 1700000002, CompletedAt: 1700000008,
			Error: &struct {
				Message string `json:"message"`
			}{Message: "Host failure"},
			Items: []ThreadItem{{
				DurationMS: new(int64(123)), Delivery: "async", ID: "item", ClientID: "submission", Type: "commandExecution",
				Questions: []Question{{ID: "question", Header: "Choice", Question: "Which one?", Title: "Pick one", IsSecret: true,
					Options: jsontext.Value(`[{"label":"A","description":"First"}]`)}},
				Text: "Exact text\n", Summary: []string{"Public summary"}, AgentThreadID: "agent", AgentPath: "/root/reader",
				Kind: "completed", Command: "printf 'a  b\\n'", Cwd: "/workspace/subdir", Source: "agent",
				ProcessID: "process", Path: "relative/file", Status: "failed", AggregatedOutput: new("Exact output\n"),
				Query: "search term", Action: &WebSearchAction{Type: "search", Query: "query", Queries: []string{"one", "two"}, URL: "https://example.test", Pattern: "needle"},
				Results: []jsontext.Value{jsontext.Value(`{"opaque":9007199254740993}`), jsontext.Value(`"verbatim"`), jsontext.Value(`null`)},
				Content: jsontext.Value(`[{"type":"text","text":"User content","unknown":9007199254740993}]`),
				Phase:   "final_answer", ExitCode: new(7),
				CommandActions: []CommandAction{{Type: "read", Command: "cat file", Path: "file", Query: "needle"}}, Changes: []FileChange{change},
				Tool: "wait", SenderThreadID: "parent", ReceiverThreadIDs: []string{"agent"}, AgentsStates: map[string]AgentState{"agent": {Status: "completed"}},
				Prompt: "Assignment", Model: "model-id", ReasoningEffort: "high",
			}},
		}},
	}
	got, err := DecodeHistoryThread(raw, "child")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history fields changed:\ngot: %#v\nwant: %#v", got, want)
	}
}

func TestDecodeHistoryThreadRejectsInvalidIdentityAndPayload(t *testing.T) {
	for _, test := range []struct{ name, raw, requested string }{
		{"different identity", `{"thread":{"id":"other"}}`, "requested"},
		{"missing identity", `{"thread":{"name":"Saved session"}}`, "requested"},
		{"empty identities", `{"thread":{"id":""}}`, ""},
		{"empty request", `{"thread":{"id":"other"}}`, ""},
		{"missing thread", `{}`, "requested"},
		{"null thread", `{"thread":null}`, "requested"},
		{"malformed payload", `{"thread":`, "requested"},
		{"invalid field", `{"thread":{"id":"requested","createdAt":"invalid"}}`, "requested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeHistoryThread([]byte(test.raw), test.requested)
			if err == nil || !reflect.DeepEqual(got, ThreadInfo{}) {
				t.Fatalf("invalid response admitted: %#v, %v", got, err)
			}
		})
	}
}

func TestHistoryItemOptionalOutcomeAndOpaqueContent(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      ThreadItem
	}{
		{"absent", `{}`, ThreadItem{}},
		{"null", `{"durationMs":null,"exitCode":null,"aggregatedOutput":null,"results":null}`, ThreadItem{}},
		{"zero", `{"durationMs":0,"exitCode":0,"aggregatedOutput":"","results":[]}`, ThreadItem{DurationMS: new(int64(0)), ExitCode: new(0), AggregatedOutput: new(""), Results: []jsontext.Value{}}},
		{"reasoning", `{"type":"reasoning","content":["Private reasoning"],"summary":["Public summary"]}`, ThreadItem{Type: "reasoning", Content: jsontext.Value(`["Private reasoning"]`), Summary: []string{"Public summary"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got ThreadItem
			if err := json.Unmarshal([]byte(test.raw), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("optional or opaque fields changed: %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestHistoryPagesPreserveIdentityTimingAndCursor(t *testing.T) {
	var threads ThreadPage
	if err := json.Unmarshal([]byte(`{"data":[{"id":"child","parentThreadId":"parent","turns":[{"id":"turn"}]}],"nextCursor":"threads-next"}`), &threads); err != nil {
		t.Fatal(err)
	}
	wantThreads := ThreadPage{Data: []ThreadInfo{{ID: "child", ParentThreadID: "parent", Turns: []HistoryTurn{{ID: "turn"}}}}, NextCursor: "threads-next"}
	if !reflect.DeepEqual(threads, wantThreads) {
		t.Fatalf("thread page = %#v, want %#v", threads, wantThreads)
	}
	var turns TurnPage
	if err := json.Unmarshal([]byte(`{"data":[{"id":"turn","status":"interrupted","startedAt":1700000001,"completedAt":1700000002,"items":[{"id":"item","type":"agentMessage","text":"Retained"}]}],"nextCursor":"turns-next"}`), &turns); err != nil {
		t.Fatal(err)
	}
	wantTurns := TurnPage{Data: []HistoryTurn{{ID: "turn", Status: "interrupted", StartedAt: 1700000001, CompletedAt: 1700000002, Items: []ThreadItem{{ID: "item", Type: "agentMessage", Text: "Retained"}}}}, NextCursor: "turns-next"}
	if !reflect.DeepEqual(turns, wantTurns) {
		t.Fatalf("turn page = %#v, want %#v", turns, wantTurns)
	}
	var items ItemPage
	if err := json.Unmarshal([]byte(`{"data":[{"turnId":"turn","item":{"id":"item","type":"agentMessage","text":"Retained"},"startedAtMs":1700000001123,"completedAtMs":1700000002456}],"nextCursor":"items-next"}`), &items); err != nil {
		t.Fatal(err)
	}
	wantItems := ItemPage{Data: []HistoryItem{{TurnID: "turn", Item: ThreadItem{ID: "item", Type: "agentMessage", Text: "Retained"}, StartedAtMs: 1700000001123, CompletedAtMs: 1700000002456}}, NextCursor: "items-next"}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("item page = %#v, want %#v", items, wantItems)
	}
	if err := json.Unmarshal([]byte(`{"data":[],"nextCursor":null}`), &items); err != nil {
		t.Fatal(err)
	}
	if items.NextCursor != "" || len(items.Data) != 0 {
		t.Fatalf("terminal item page retained prior values: %#v", items)
	}
}
