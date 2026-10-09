package router

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestJournalToolIsAbsentInBothModes(t *testing.T) {
	t.Parallel()
	_, _, codeMode, _ := newMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
	_, native := newTopLevelMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
	for name, request := range map[string]*parsedResponsesRequest{"exec": codeMode, "native": native} {
		t.Run(name, func(t *testing.T) {
			var tools []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.fields["tools"], &tools); err != nil {
				t.Fatal(err)
			}
			for _, tool := range tools {
				if tool.Name == journalToolName {
					t.Fatal("dedicated journal tool exposed")
				}
			}
		})
	}
}

func TestCodeModeJournalCompletionAvoidsProviderContinuation(t *testing.T) {
	t.Parallel()
	final := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "The assigned work is complete."}}}
	journalCall := func(callID, arguments string) map[string]any {
		return map[string]any{"type": "function_call", "id": callID + "-item", "call_id": callID, "namespace": "functions", "name": "journal", "arguments": arguments, "status": "completed"}
	}
	exec := map[string]any{"type": "custom_tool_call", "id": "exec-item", "call_id": "exec-call", "namespace": "functions", "name": "exec", "status": "completed",
		"input": `await tools.mcp__mekugi__journal_mutate({mutations:[{op:"log",text:"Validated milestone"}]}); await tools.exec_command({cmd: "true"});`}
	for _, test := range []struct {
		name     string
		native   bool
		calls    []any
		requests int
		flush    bool
		hint     string // call ID whose result must carry the exec hint
		plain    string // call ID whose result must not carry it
	}{
		{name: "exec journal is host work", calls: []any{exec}, requests: 1},
		{name: "natural final answer", calls: []any{final}, requests: 1},
		{name: "stray add with final answer", calls: []any{journalCall("add-call", `{"op":"add","text":"Validated milestone"}`), final}, requests: 1, flush: true, hint: "add-call"},
		{name: "stray finish", calls: []any{journalCall("finish-call", `{"op":"finish","journal":[{"op":"add","text":"Validated milestone"}]}`)}, requests: 1, flush: true, hint: "finish-call"},
		// A journal-only list result stays inspectable, so it continues.
		{name: "list", calls: []any{journalCall("list-call", `{"op":"list"}`)}, requests: 2, plain: "list-call"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(test.name+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				store, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				proxy.replayStore = store
				workspace := t.TempDir()
				snapshot := "full"
				if stream {
					snapshot = "empty"
				}
				provider := &serverFakeProvider{results: []serverForwardResult{
					{response: journalFinishResponse(t, stream, "completed", snapshot, test.calls...)},
					{response: journalFinishResponse(t, stream, "completed", snapshot, final)},
				}}
				request := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = stream
					if test.native {
						fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
						fields["tools"] = testExecResponsesTools()
					}
				})
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != test.requests {
					t.Fatalf("provider requests = %d, want %d", len(provider.forwarded), test.requests)
				}
				if got := strings.Contains(output.String(), "Journal flush"); got != test.flush {
					t.Fatalf("journal flush = %t, want %t: %s", got, test.flush, output.Bytes())
				}
				if test.name == "natural final answer" || test.name == "list" {
					if !bytes.Contains(output.Bytes(), []byte(`"id":"answer-item"`)) {
						t.Fatalf("answer-only Main completion replaced the ordinary provider final: %s", output.Bytes())
					}
					items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
					if err != nil || len(items) != 1 || items[0].Text != "The assigned work is complete." || !items[0].Flushed {
						t.Fatalf("ordinary final was not durably captured and acknowledged: %+v, %v", items, err)
					}
				}
				results := make(map[string]map[string]json.RawMessage)
				dispatched := false
				for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
					if callID := journalResultCallID(item); callID != "" {
						var result map[string]json.RawMessage
						if err := json.Unmarshal([]byte(jsonString(item, "output")), &result); err != nil {
							t.Fatal(err)
						}
						results[callID] = result
					}
					dispatched = dispatched || jsonString(item, "call_id") == "exec-call"
				}
				if test.calls[0].(map[string]any)["type"] == "custom_tool_call" && !dispatched {
					t.Fatalf("exec was not returned to the host: %s", output.Bytes())
				}
				if test.hint != "" {
					result := results[test.hint]
					if string(result["ok"]) != "true" || jsonString(result, "hint") != codeModeJournalHint {
						t.Fatalf("stray exec journal result = %s, want applied with hint", mustMarshalJSON(result))
					}
				}
				if test.plain != "" {
					result := results[test.plain]
					if string(result["ok"]) != "true" || result["hint"] != nil {
						t.Fatalf("journal result = %s, want ok without hint", mustMarshalJSON(result))
					}
				}
				if test.hint != "" || test.name == "native add" {
					items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
					if err != nil || !slices.ContainsFunc(items, func(item journalItem) bool { return item.Text == "Validated milestone" }) {
						t.Fatalf("journal mutation was not applied: %+v, %v", items, err)
					}
				}
			})
		}
	}
}
