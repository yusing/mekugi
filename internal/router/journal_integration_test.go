package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestJournalRouterToolContinuesWithoutClientDispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			workspace := t.TempDir()
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			call := map[string]any{"type": "function_call", "id": "item-journal", "call_id": "journal-call", "name": "journal", "arguments": `{"op":"add","text":"Verified the journal path"}`, "status": "completed"}
			first := mustTestJSON(t, map[string]any{"id": "response-journal", "status": "completed", "output": []any{call}})
			second := mustTestJSON(t, map[string]any{
				"id": "response-answer", "status": "completed",
				"output": []any{journalFinishCall(`{"op":"finish"}`)},
				"usage":  map[string]any{"input_tokens": 20, "output_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 12}, "output_tokens_details": map[string]any{"reasoning_tokens": 3}},
			})
			response1 := serverHTTPResponse(string(first))
			response2 := serverHTTPResponse(string(second))
			if stream {
				response1 = serverHTTPResponse(finalAnswerTestWire([][]byte{
					mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}),
					mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(first)}),
				}))
				var object struct {
					Output []json.RawMessage `json:"output"`
				}
				if err := json.Unmarshal(second, &object); err != nil {
					t.Fatal(err)
				}
				response2 = serverHTTPResponse(finalAnswerTestWire([][]byte{
					mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": object.Output[0]}),
					mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(second)}),
				}))
				response1.Header.Set("Content-Type", "text/event-stream")
				response2.Header.Set("Content-Type", "text/event-stream")
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response1}, {response: response2}}}
			var output bytes.Buffer
			issues := NewCriticalErrors()
			if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, issues, proxy, nil, nil); err != nil {
				t.Fatal(err)
			}
			if len(issues.entries) != 0 {
				t.Fatalf("successful continuation recorded failure notices: %+v", issues.entries)
			}
			if len(provider.forwarded) != 2 {
				t.Fatalf("provider requests: %d", len(provider.forwarded))
			}
			if !bytes.Contains(provider.forwarded[1], []byte("function_call_output")) || !bytes.Contains(provider.forwarded[1], []byte("j1")) {
				t.Fatal("journal result missing from model continuation")
			}
			if strings.Contains(output.String(), `"phase":"final_answer"`) {
				t.Fatalf("finish produced a separate final-answer message: %s", output.String())
			}
			if !strings.Contains(output.String(), "Verified the journal path") || !strings.Contains(output.String(), "Tokens:") {
				t.Fatalf("missing terminal record: %s", output.String())
			}
			items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
			if err != nil || len(items) != 1 || !items[0].Reported {
				t.Fatalf("delivery acknowledgement: %+v %v", items, err)
			}
		})
	}
}

func TestJournalInterruptedSnapshotRetainsExecutedResult(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			call := map[string]any{"type": "function_call", "id": "journal-item", "call_id": "journal-call", "name": "journal", "arguments": `{"op":"list"}`}
			ordinary := map[string]any{"type": "function_call", "id": "ordinary", "call_id": "ordinary-call", "name": "lookup", "arguments": `{}`, "status": "completed"}
			for _, item := range []any{call, ordinary} {
				if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})); err != nil {
					t.Fatal(err)
				}
			}
			events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type":     "response." + status,
				"response": map[string]any{"id": "interrupted", "status": status, "output": []any{}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var terminal struct {
				Response struct {
					Output []map[string]json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if len(events) == 0 {
				t.Fatal("missing interrupted terminal")
			}
			if err := json.Unmarshal(events[len(events)-1], &terminal); err != nil {
				t.Fatal(err)
			}
			output := terminal.Response.Output
			if len(output) != 2 || journalResultCallID(output[0]) != "journal-call" || jsonString(output[1], "call_id") != "ordinary-call" {
				t.Fatalf("interrupted snapshot lost completed output: %s", mustTestJSON(t, output))
			}
		})
	}
}

func TestJournalLiveReportRemainsEligibleForTerminalFlush(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	var err error
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "add", []journalMutation{{Op: "add", Text: new("Tests passed"), ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	checkState := func(reported, flushed bool) {
		t.Helper()
		// Read through a fresh store, rather than relying on the delivery cache.
		items, err := newJournalStore().list(t.Context(), proxy.replayStore, workspace, "thread-1")
		if err != nil || len(items) != 1 || items[0].Reported != reported || items[0].Flushed != flushed {
			t.Fatalf("journal state: %+v, err=%v; want reported=%v flushed=%v", items, err, reported, flushed)
		}
	}
	deliver := func(terminal, acknowledge bool, want string) {
		t.Helper()
		messages, err := transform.prepareJournalDelivery(terminal)
		if err != nil {
			t.Fatal(err)
		}
		defer transform.ReleaseDelivery()
		if want == "" {
			if len(messages) != 0 {
				t.Fatalf("unexpected repeated delivery: %s", mustTestJSON(t, messages))
			}
			return
		}
		if len(messages) != 1 || commentaryMessageText(messages[0]) != want {
			t.Fatalf("journal display: %s; want %q", mustTestJSON(t, messages), want)
		}
		if acknowledge {
			transform.Delivered(assistantCommentaryDoneEvent(messages[0]))
		}
	}
	live := "Journal update `/root` (`j1`)\nTests passed"
	flush := "Journal flush `/root`\n- `j1`\n\n  Tests passed\n"
	deliver(false, false, live)
	checkState(false, false)
	deliver(false, true, live)
	checkState(true, false)
	deliver(false, true, "")
	deliver(true, false, flush)
	checkState(true, false)
	deliver(true, true, flush)
	checkState(true, true)
	deliver(true, true, "")
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "edit", []journalMutation{{Op: "edit", ID: "j1", Text: new("Tests passed again")}}); err != nil {
		t.Fatal(err)
	}
	checkState(false, false)
	deliver(false, true, "")
	deliver(true, true, "Journal flush `/root`\n- `j1`\n\n  Tests passed again\n")
	checkState(true, true)
}

func TestJournalKnownAncestryOnly(t *testing.T) {
	activity := newSubagentActivity()
	for _, node := range []struct {
		thread, parent, path string
		child                bool
	}{
		{"root", "", "/root", false},
		{"a", "root", "/root/a", true},
		{"b", "root", "/root/b", true},
		{"nested", "a", "/root/a/nested", true},
		{"other", "", "/root", false},
	} {
		if !activity.observe(node.thread, node.parent, node.path, node.child) {
			t.Fatal("identity rejected")
		}
	}
	if got, err := activity.journalThread("a", "/root"); err != nil || got != "root" {
		t.Fatalf("ancestor: %q %v", got, err)
	}
	if got, err := activity.journalThread("root", "/root/a/nested"); err != nil || got != "nested" {
		t.Fatalf("descendant: %q %v", got, err)
	}
	for _, query := range []struct{ caller, path string }{{"a", "/root/b"}, {"other", "/root/a"}, {"missing", "/root"}} {
		if _, err := activity.journalThread(query.caller, query.path); err == nil {
			t.Fatalf("unproven access: %+v", query)
		}
	}
}

func TestJournalCatalogStripsPlansButKeepsHistory(t *testing.T) {
	historical := json.RawMessage(`{"type":"function_call","call_id":"old","name":"update_plan","arguments":"{\"plan\":[]}"}`)
	fields := map[string]json.RawMessage{
		"tools": json.RawMessage(`[{"type":"function","name":"update_plan"},{"type":"function","name":"lookup","strict":true,"parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]`),
		"input": mustTestJSON(t, []any{
			historical,
			map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "update_plan"}, map[string]any{"type": "function", "name": "clock"}}}}},
		}),
	}
	catalog := decodeResponsesToolCatalog(fields)
	strict := mustTestJSON(t, catalog.top.tools[1])
	if err := stripStockPlanTools(fields, catalog); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fields["tools"], []byte("update_plan")) || !bytes.Contains(fields["tools"], strict) {
		t.Fatalf("top catalog: %s", fields["tools"])
	}
	var input []json.RawMessage
	if err := json.Unmarshal(fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input[0], historical) || bytes.Contains(input[1], []byte("update_plan")) {
		t.Fatalf("history or additional tools: %s", fields["input"])
	}
}

func TestJournalNamedResultDiscoversDurableReplay(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	proxy.replayStore = store
	workspace := t.TempDir()
	callID := "journal-call"
	call := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON(callID),
		"name": mustMarshalJSON(journalToolName), "arguments": mustMarshalJSON(`{"op":"list"}`),
	}
	history := mekugiHistory{toolName: journalHistoryTool, upstreamItem: call, carrierName: journalToolName, carrierKind: codeModeCarrierFunction}
	if err := proxy.replayStore.put(t.Context(), workspace, map[string]mekugiHistory{callID: history}); err != nil {
		t.Fatal(err)
	}
	result := journalClientResult(map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call_output"), "call_id": mustMarshalJSON(callID),
		"output": mustMarshalJSON(`{"ok":true,"items":[]}`),
	})
	request := serverRequest(t, func(fields map[string]any) { fields["input"] = []any{result} })
	visible, err := proxy.reconcileVisibleInput(t.Context(), &request, workspace, "resumed")
	if err != nil {
		t.Fatal(err)
	}
	if visible[callID].toolName != journalHistoryTool {
		t.Fatal("named output did not recover durable call identity")
	}
	if err := restoreJournalCalls(&request, visible); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil || len(input) != 2 {
		t.Fatalf("missing durable pair: %s %v", request.fields["input"], err)
	}
}

func TestJournalNamedResultRestoresAndRebasesCachedInput(t *testing.T) {
	call := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("journal-call"),
		"name": mustMarshalJSON(journalToolName), "arguments": mustMarshalJSON(`{"op":"list"}`),
	}
	result := journalClientResult(map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call_output"), "call_id": mustMarshalJSON("journal-call"),
		"output": mustMarshalJSON(`{"ok":true,"items":[]}`),
	})
	if _, exists := result["call_id"]; exists || jsonString(result, "name") != journalToolName || !strings.HasPrefix(jsonString(result, "id"), "fco_") {
		t.Fatalf("result cannot survive Codex normalization: %s", mustMarshalJSON(result))
	}
	ordinary := map[string]json.RawMessage{"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("ordinary")}
	request := serverRequest(t, func(fields map[string]any) {
		fields["input"] = []any{map[string]any{"role": "user", "content": "work"}, result, ordinary}
	})
	request.cachedInput = 3
	visible := map[string]mekugiHistory{"journal-call": {toolName: journalHistoryTool, upstreamItem: call}}
	if err := restoreJournalCalls(&request, visible); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 4 || jsonString(input[1], "type") != "function_call" || jsonString(input[2], "call_id") != "journal-call" || request.cachedInput != 0 || !request.rebaseInput {
		t.Fatalf("incorrect restored prefix: %s cached=%d rebase=%v", request.fields["input"], request.cachedInput, request.rebaseInput)
	}
	if err := restoreJournalCalls(&request, visible); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request.fields["input"], &input); err != nil || len(input) != 4 {
		t.Fatalf("replay duplicated call: %s %v", request.fields["input"], err)
	}
}

func TestJournalCapacityDoesNotRejectUnrelatedRequest(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			for i := range maxJournalThreads {
				if err := proxy.journals.initialize(t.Context(), nil, "workspace", fmt.Sprint(i), "/root", ""); err != nil {
					t.Fatal(err)
				}
			}
			workspace := t.TempDir()
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
			answer := map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer survives capacity"}}}
			response := mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{answer}})
			httpResponse := serverHTTPResponse(string(response))
			if stream {
				httpResponse = serverHTTPResponse(finalAnswerTestWire([][]byte{
					mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": answer}),
					mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer}),
					mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(response)}),
				}))
				httpResponse.Header.Set("Content-Type", "text/event-stream")
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: httpResponse}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "new", provider, &output, nil, proxy, nil, nil); err != nil {
				t.Fatalf("journal capacity blocked unrelated request: %v", err)
			}
			transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
			result, err := transform.executeJournalCall(map[string]json.RawMessage{
				"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, "journal"),
				"call_id": mustTestJSON(t, "finish-at-capacity"), "arguments": mustTestJSON(t, `{"op":"finish"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(jsonString(result, "output"), "journal terminal delivery unavailable") || transform.journalTerminalReady() {
				t.Fatalf("finish succeeded without journal state: %s", mustTestJSON(t, result))
			}
			if !strings.Contains(output.String(), "Answer survives capacity") {
				t.Fatalf("capacity discarded provider answer: %s", output.String())
			}
		})
	}
}

func TestJournalTerminalRetentionFailureDoesNotSucceedSilently(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			store.maxCommentaryBytes = 1
			workspace := t.TempDir()
			request := serverRequest(t, nil)
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
			if child {
				headers.Set(codexTurnMetadataHeader, string(mustMarshalJSON(codexTurnMetadata{
					RequestKind: "turn", SubagentKind: "thread_spawn", AgentName: "/root/child", Directories: map[string]json.RawMessage{workspace: nil},
				})))
			}
			if err := proxy.journals.initialize(t.Context(), store, workspace, "thread-1", "/root", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), store, workspace, "thread-1", "add", []journalMutation{{Op: "add", Text: new("Must not be silently lost")}}); err != nil {
				t.Fatal(err)
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, false, "completed", "full", journalFinishCall(`{"op":"finish"}`))}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "quota", provider, &output, nil, proxy, nil, nil); err == nil {
				t.Fatal("terminal succeeded after required journal retention failed")
			}
			items, err := proxy.journals.list(t.Context(), store, workspace, "thread-1")
			if err != nil || len(items) != 1 || items[0].Reported {
				t.Fatalf("failed retention marked item reported: %+v %v", items, err)
			}
		})
	}
}

func TestJournalToolReturnsBatchedIDs(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	workspace := t.TempDir()
	request := serverRequest(t, nil)
	transform, err := proxy.prepareRequest(t.Context(), &request, "batch-ids", "thread-1", codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	for _, test := range []struct {
		call string
		args string
		want string
	}{
		{"add", `{"op":"add","text":"Main milestone","journal":[{"op":"add","text":"Batched milestone"}]}`, `{"ok":true,"id":"j2","journal_ids":["j1"]}`},
		{"list", `{"op":"list","journal":[{"op":"add","text":"Before listing"}]}`, ""},
	} {
		item := map[string]json.RawMessage{
			"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
			"call_id": mustMarshalJSON(test.call), "arguments": mustMarshalJSON(test.args),
		}
		result, err := transform.executeJournalCall(item)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal([]byte(jsonString(result, "output")), &got); err != nil {
			t.Fatal(err)
		}
		if test.want != "" {
			var want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if string(mustMarshalJSON(got)) != string(mustMarshalJSON(want)) {
				t.Fatalf("add result = %s", result["output"])
			}
		} else {
			var items []journalListItem
			if err := json.Unmarshal(got["items"], &items); err != nil || len(items) != 3 || string(got["journal_ids"]) != `["j3"]` {
				t.Fatalf("list result = %s, error = %v", result["output"], err)
			}
		}
	}
}

func TestJournalLiveSnapshotCacheRefreshesOnMutationAndTerminal(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = replay
	workspace := t.TempDir()
	request := serverRequest(t, nil)
	transform, err := proxy.prepareRequest(t.Context(), &request, "live-cache", "thread-1", codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	if _, err := proxy.journals.apply(t.Context(), replay, workspace, "thread-1", "", []journalMutation{{Op: "add", Text: new("Silent milestone")}}); err != nil {
		t.Fatal(err)
	}
	messages, err := transform.prepareJournalDelivery(false)
	if err != nil || len(messages) != 0 || transform.journalQuietFile == nil {
		t.Fatalf("quiet snapshot not cached: %d %v", len(messages), err)
	}
	other := newJournalStore()
	if _, err := other.apply(t.Context(), replay, workspace, "thread-1", "", []journalMutation{{Op: "edit", ID: "j1", Text: new("Show revised milestone"), ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	messages, err = transform.prepareJournalDelivery(false)
	if err != nil || len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Show revised milestone") {
		t.Fatalf("external mutation missed: %v %v", messages, err)
	}
	transform.ReleaseDelivery()
	// Exhausted live delivery must also use the cache even with pending notices.
	transform.journalLiveBytes = maxCommentaryPublicationBytes
	messages, err = transform.prepareJournalDelivery(false)
	if err != nil || len(messages) != 0 || transform.journalQuietFile == nil {
		t.Fatalf("exhausted live snapshot not cached: %d %v", len(messages), err)
	}
	messages, err = transform.prepareJournalDelivery(true)
	if err != nil || len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Show revised milestone") {
		t.Fatalf("terminal failed to bypass live cache: %v %v", messages, err)
	}
	transform.ReleaseDelivery()
}

func TestJournalCatalogRejectsCollisions(t *testing.T) {
	for _, tools := range []string{
		`[{"type":"function","name":"journal"}]`,
		`[{"type":"function","name":"functions.journal"}]`,
		`[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"journal"}]}]`,
		`[{"type":"namespace","name":"outer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"journal"}]}]}]`,
	} {
		for _, additional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/additional=%t", tools, additional), func(t *testing.T) {
				fields := map[string]json.RawMessage{"tools": json.RawMessage(tools)}
				if additional {
					fields = map[string]json.RawMessage{"input": mustTestJSON(t, []any{map[string]any{"type": "additional_tools", "tools": json.RawMessage(tools)}})}
				}
				before := mustTestJSON(t, fields)
				if err := exposeJournalTool(fields, decodeResponsesToolCatalog(fields)); err == nil {
					t.Fatal("accepted journal collision")
				}
				if !bytes.Equal(before, mustTestJSON(t, fields)) {
					t.Fatal("rejected exposure changed catalog")
				}
			})
		}
	}
}

func TestJournalToolSchemaIncludesBatchedMutations(t *testing.T) {
	fields := map[string]json.RawMessage{}
	catalog := decodeResponsesToolCatalog(fields)
	if err := exposeJournalTool(fields, catalog); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(catalog.top.tools[0].rawField("parameters"), &schema); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(schema.Properties["journal"], journalMutationsSchema()) {
		t.Fatalf("missing or incorrect journal schema: %s", schema.Properties["journal"])
	}
}

func TestJournalFlushNestsMultilineMarkdown(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	body := "Result\n\n- first\n  - nested\n\n1. ordered\n2. next\n\n```go\nx := 1\n```\n\nParagraph."
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "", []journalMutation{
		{Op: "add", Text: new(body)},
		{Op: "add", Text: new("Second item")},
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := transform.prepareJournalDelivery(true)
	defer transform.ReleaseDelivery()
	want := "Journal flush `/root`\n- `j1`\n\n  Result\n  \n  - first\n    - nested\n  \n  1. ordered\n  2. next\n  \n  ```go\n  x := 1\n  ```\n  \n  Paragraph.\n\n- `j2`\n\n  Second item\n"
	if err != nil || len(messages) != 1 || commentaryMessageText(messages[0]) != want {
		t.Fatalf("flush: %s %v; want %q", mustTestJSON(t, messages), err, want)
	}
}

func TestJournalFailedCompletedEnvelopeRetainsPendingRevisions(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, testTranslator(t, new(int)))
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "seed",
		[]journalMutation{{Op: "add", Text: new("Pending milestone")}}); err != nil {
		t.Fatal(err)
	}
	answer := map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer",
		"content": []any{map[string]any{"type": "output_text", "text": "Interrupted output"}}}
	if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer})); err != nil {
		t.Fatal(err)
	}
	events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
		"type": "response.completed", "response": map[string]any{"id": "failed", "status": "failed", "output": []any{answer}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	wire := string(bytes.Join(events, nil))
	if !strings.Contains(wire, "response.failed") || !strings.Contains(wire, "Interrupted output") || strings.Contains(wire, "Journal flush") {
		t.Fatalf("failed terminal lost output or flushed the journal: %s", wire)
	}
	for _, event := range events {
		transform.Delivered(event)
	}
	transform.ReleaseDelivery()
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, transform.shellThreadID)
	if err != nil || len(items) != 1 || items[0].Flushed || len(transform.journalDeliveries) != 0 {
		t.Fatalf("failed terminal changed delivery state: %+v, %v", items, err)
	}
}
