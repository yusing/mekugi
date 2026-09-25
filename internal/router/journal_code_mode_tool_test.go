package router

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func preparedJournalToolParameters(t *testing.T, request *parsedResponsesRequest) (ops []string, properties map[string]json.RawMessage) {
	t.Helper()
	var tools []struct {
		Name       string `json:"name"`
		Parameters struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(request.fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name != journalToolName {
			continue
		}
		var op struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(tool.Parameters.Properties["op"], &op); err != nil {
			t.Fatal(err)
		}
		return op.Enum, tool.Parameters.Properties
	}
	t.Fatalf("prepared request has no journal tool: %s", request.fields["tools"])
	return nil, nil
}

// A dedicated mutation in Code Mode is a standalone provider round trip; the
// exec-local journal helper records the same mutation alongside useful work.
func TestJournalToolOffersOnlyListInCodeMode(t *testing.T) {
	_, _, codeMode, _ := newMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
	ops, properties := preparedJournalToolParameters(t, codeMode)
	if !slices.Equal(ops, []string{"list"}) {
		t.Fatalf("Code Mode journal ops = %v, want only list", ops)
	}
	for _, name := range []string{"id", "text", "journal", "report_now"} {
		if _, present := properties[name]; present {
			t.Errorf("Code Mode journal schema exposes mutation property %q", name)
		}
	}
	if _, present := properties["agent"]; !present {
		t.Error("Code Mode journal list lost its agent selector")
	}

	_, native := newNativeMekugiTestTransformWithProxy(t, newManagedMekugiProxy(t))
	ops, properties = preparedJournalToolParameters(t, native)
	if !slices.Equal(ops, []string{"list", "add", "edit", "delete"}) {
		t.Fatalf("native journal ops = %v, want list and mutations", ops)
	}
	for _, name := range []string{"id", "text", "journal", "report_now", "agent"} {
		if _, present := properties[name]; !present {
			t.Errorf("native journal schema lost property %q", name)
		}
	}
}

func TestCodeModeJournalCompletionAvoidsProviderContinuation(t *testing.T) {
	final := map[string]any{"type": "message", "id": "answer-item", "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "The assigned work is complete."}}}
	journalCall := func(callID, arguments string) map[string]any {
		return map[string]any{"type": "function_call", "id": callID + "-item", "call_id": callID, "namespace": "functions", "name": "journal", "arguments": arguments, "status": "completed"}
	}
	exec := map[string]any{"type": "custom_tool_call", "id": "exec-item", "call_id": "exec-call", "namespace": "functions", "name": "exec", "status": "completed",
		"input": `const id = await journal({op: "add", text: "Validated milestone"}); await tools.exec_command({cmd: "true"});`}
	for _, test := range []struct {
		name     string
		native   bool
		calls    []any
		requests int
		flush    bool
		hint     string // call ID whose result must carry the Code Mode hint
		plain    string // call ID whose result must not carry it
	}{
		{name: "exec journal is host work", calls: []any{exec}, requests: 1},
		{name: "natural final answer", calls: []any{final}, requests: 1, flush: true},
		{name: "stray add with final answer", calls: []any{journalCall("add-call", `{"op":"add","text":"Validated milestone"}`), final}, requests: 1, flush: true, hint: "add-call"},
		{name: "stray finish", calls: []any{journalCall("finish-call", `{"op":"finish","journal":[{"op":"add","text":"Validated milestone"}]}`)}, requests: 1, flush: true, hint: "finish-call"},
		// A journal-only list result stays inspectable, so it continues.
		{name: "list", calls: []any{journalCall("list-call", `{"op":"list"}`)}, requests: 2, flush: true, plain: "list-call"},
		{name: "native add", native: true, calls: []any{journalCall("add-call", `{"op":"add","text":"Validated milestone"}`)}, requests: 2, flush: true, plain: "add-call"},
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
						fields["tools"] = testNativeResponsesTools()
					}
				})
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != test.requests {
					t.Fatalf("provider requests = %d, want %d", len(provider.forwarded), test.requests)
				}
				if got := strings.Contains(output.String(), "Journal flush"); got != test.flush {
					t.Fatalf("journal flush = %t, want %t: %s", got, test.flush, output.Bytes())
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
						t.Fatalf("stray Code Mode journal result = %s, want applied with hint", mustMarshalJSON(result))
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
