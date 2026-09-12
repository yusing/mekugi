package router

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
)

func journalFinishResponse(t *testing.T, stream bool, status, snapshot string, calls ...any) *http.Response {
	t.Helper()
	terminal := map[string]any{"id": "finish-response", "status": status}
	switch snapshot {
	case "full", "snapshot-only":
		terminal["output"] = calls
	case "empty":
		terminal["output"] = []any{}
	}
	if !stream {
		return serverHTTPResponse(string(mustTestJSON(t, terminal)))
	}
	var events [][]byte
	for _, call := range calls {
		if snapshot == "snapshot-only" {
			break
		}
		events = append(events, mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}))
	}
	events = append(events, mustTestJSON(t, map[string]any{"type": "response." + status, "response": terminal}))
	response := serverHTTPResponse(finalAnswerTestWire(events))
	response.Header.Set("Content-Type", "text/event-stream")
	return response
}

func journalFinishCall(arguments string) map[string]any {
	return map[string]any{"type": "function_call", "id": "finish-item", "call_id": "finish-call", "namespace": "functions", "name": "journal", "arguments": arguments, "status": "completed"}
}

func journalFinishClientOutput(t *testing.T, stream bool, wire []byte) []map[string]json.RawMessage {
	t.Helper()
	body := wire
	if stream {
		body = nil
		for line := range strings.SplitSeq(string(wire), "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok || data == "[DONE]" {
				continue
			}
			var event map[string]json.RawMessage
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			switch jsonString(event, "type") {
			case "response.completed", "response.failed", "response.incomplete":
				body = event["response"]
			}
		}
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode client terminal: %v; wire=%s", err, wire)
	}
	return response.Output
}

func TestJournalFinishEndsWithoutProviderContinuation(t *testing.T) {
	for _, mode := range []string{"json", "sse-full", "sse-empty", "sse-absent", "sse-snapshot-only"} {
		for _, batched := range []bool{false, true} {
			name := mode + map[bool]string{false: "/existing", true: "/batched"}[batched]
			t.Run(name, func(t *testing.T) {
				stream := mode != "json"
				snapshot := strings.TrimPrefix(mode, "sse-")
				if !stream {
					snapshot = "full"
				}
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
				proxy.journals = newJournalStore()
				var err error
				proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				workspace := t.TempDir()
				if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "thread-1", "/root", ""); err != nil {
					t.Fatal(err)
				}
				arguments := `{"op":"finish"}`
				if batched {
					arguments = `{"op":"finish","journal":[{"op":"add","text":"Completed the assigned milestone"}]}`
				} else if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "seed", []journalMutation{{Op: "add", Text: new("Completed the assigned milestone")}}); err != nil {
					t.Fatal(err)
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, stream, "completed", snapshot, journalFinishCall(arguments))}}}
				request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 {
					t.Fatalf("finish issued %d provider requests; want 1", len(provider.forwarded))
				}
				if !strings.Contains(output.String(), "Journal flush") || !strings.Contains(output.String(), "Completed the assigned milestone") {
					t.Fatalf("missing journal flush: %s", output.Bytes())
				}
				items, err := newJournalStore().list(t.Context(), proxy.replayStore, workspace, "thread-1")
				if err != nil || len(items) != 1 || !items[0].Reported || !items[0].Flushed {
					t.Fatalf("durable flush acknowledgement: %+v, %v", items, err)
				}
				found := false
				for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
					if jsonString(item, "type") == "function_call" {
						t.Fatalf("router finish escaped as client-dispatched call: %s", mustTestJSON(t, item))
					}
					if journalResultCallID(item) == "finish-call" {
						var result struct {
							OK              bool     `json:"ok"`
							FinishRequested bool     `json:"finish_requested"`
							JournalIDs      []string `json:"journal_ids"`
						}
						if err := json.Unmarshal([]byte(jsonString(item, "output")), &result); err != nil || !result.OK || !result.FinishRequested {
							t.Fatalf("finish result: %s, %v", mustTestJSON(t, item), err)
						}
						if batched && (len(result.JournalIDs) != 1 || result.JournalIDs[0] != "j1") {
							t.Fatalf("missing assigned mutation ID: %+v", result)
						}
						found = true
					}
				}
				if !found {
					t.Fatalf("terminal lost retained finish result: %s", output.Bytes())
				}
			})
		}
	}
}

func TestJournalFinishDoesNotHidePendingCallsOrFlushFailures(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"mixed", "failed", "incomplete", "invalid-id", "invalid-text", "invalid-agent", "invalid-report-now"} {
			t.Run(map[bool]string{false: "json/", true: "sse/"}[stream]+scenario, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
				workspace := t.TempDir()
				arguments := `{"op":"finish"}`
				invalid := strings.HasPrefix(scenario, "invalid-")
				if invalid {
					arguments = map[string]string{"invalid-id": `{"op":"finish","id":"j1"}`, "invalid-text": `{"op":"finish","text":"not allowed"}`, "invalid-agent": `{"op":"finish","agent":"/root"}`, "invalid-report-now": `{"op":"finish","report_now":true}`}[scenario]
				}
				// Seed through a separate router-owned call in the same response.
				seed := map[string]any{"type": "function_call", "id": "seed-item", "call_id": "seed-call", "name": "journal", "arguments": `{"op":"add","text":"Unflushed milestone"}`, "status": "completed"}
				pending := map[string]any{"type": "function_call", "id": "lookup-item", "call_id": "lookup-call", "name": "lookup", "arguments": `{}`, "status": "completed"}
				calls := []any{seed, journalFinishCall(arguments)}
				status := "completed"
				if scenario == "mixed" {
					calls = append(calls, pending)
				} else if scenario == "failed" || scenario == "incomplete" {
					status = scenario
				}
				snapshot := "full"
				if stream {
					snapshot = "empty"
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, stream, status, snapshot, calls...)}}}
				if invalid {
					provider.results = append(provider.results, serverForwardResult{response: journalFinishResponse(t, stream, "completed", snapshot, pending)})
				}
				request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
				var output bytes.Buffer
				err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil, nil)
				if status == "completed" && err != nil {
					t.Fatal(err)
				}
				wantRequests := 1
				if invalid {
					wantRequests = 2
					if len(provider.forwarded) == 2 && !bytes.Contains(provider.forwarded[1], []byte(`\"ok\":false`)) {
						t.Fatalf("invalid finish error missing from continuation: %s", provider.forwarded[1])
					}
				}
				if len(provider.forwarded) != wantRequests {
					t.Fatalf("provider requests: %d; want %d", len(provider.forwarded), wantRequests)
				}
				if strings.Contains(output.String(), "Journal flush") {
					t.Fatalf("ineligible response flushed: %s", output.Bytes())
				}
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
				if err != nil || len(items) != 1 || items[0].Flushed {
					t.Fatalf("journal state: %+v, %v", items, err)
				}
				if scenario == "mixed" || invalid {
					found := false
					for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
						found = found || (jsonString(item, "type") == "function_call" && jsonString(item, "call_id") == "lookup-call")
					}
					if !found {
						t.Fatalf("pending client call lost: %s", output.Bytes())
					}
				}
			})
		}
	}
}

func TestJournalFinishReplayDoesNotFinishLaterCRUD(t *testing.T) {
	workspace, storeDirectory := t.TempDir(), t.TempDir()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	var err error
	proxy.replayStore, err = openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, false, "completed", "full", journalFinishCall(`{"op":"finish","journal":[{"op":"add","text":"First task completed"}]}`))}}}
	var completed bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "original-session", provider, &completed, NewCriticalErrors(), proxy, nil, nil); err != nil {
		t.Fatal(err)
	}
	history := journalFinishClientOutput(t, false, completed.Bytes())
	resumed := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	resumed.replayStore, err = openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	request := serverRequest(t, func(fields map[string]any) {
		input := fields["input"].([]any)
		for _, item := range history {
			input = append(input, item)
		}
		fields["input"] = append(input, map[string]any{"role": "user", "content": "Start another task"})
	})
	add := map[string]any{"type": "function_call", "id": "new-item", "call_id": "new-call", "name": "journal", "arguments": `{"op":"add","text":"Second task in progress"}`, "status": "completed"}
	pending := map[string]any{"type": "function_call", "id": "lookup-item", "call_id": "lookup-call", "name": "lookup", "arguments": `{}`, "status": "completed"}
	provider = &serverFakeProvider{results: []serverForwardResult{
		{response: journalFinishResponse(t, false, "completed", "full", add)},
		{response: journalFinishResponse(t, false, "completed", "full", pending)},
	}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "fresh-session", provider, &output, NewCriticalErrors(), resumed, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 2 {
		t.Fatalf("replayed finish terminated ordinary CRUD; provider requests=%d", len(provider.forwarded))
	}
	if !bytes.Contains(provider.forwarded[0], []byte(`\"op\":\"finish\"`)) {
		t.Fatal("fresh router did not restore the durable finish call")
	}
	if strings.Contains(output.String(), "Journal flush") || !strings.Contains(output.String(), "lookup-call") {
		t.Fatalf("replayed finish affected current delivery: %s", output.Bytes())
	}
	items, err := newJournalStore().list(t.Context(), resumed.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 2 || !items[0].Flushed || items[1].Flushed {
		t.Fatalf("replay changed journal delivery state: %+v, %v", items, err)
	}
}

func TestJournalBatchedErrorsAreCorrectable(t *testing.T) {
	for _, batch := range []string{
		`{}`,
		`[{"op":"edit","id":"j9","text":"missing"}]`,
		`[{"op":"add","text":"must roll back"},{"op":"edit","id":"j9","text":"missing"}]`,
	} {
		t.Run(batch, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			call := journalFinishCall(`{"op":"finish","journal":` + batch + `}`)
			var item map[string]json.RawMessage
			if err := json.Unmarshal(mustTestJSON(t, call), &item); err != nil {
				t.Fatal(err)
			}
			result, err := transform.executeJournalCall(item)
			if err != nil {
				t.Fatalf("model mistake became a turn failure: %v", err)
			}
			var outcome struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(jsonString(result, "output")), &outcome); err != nil || outcome.OK || outcome.Error == "" {
				t.Fatalf("missing correctable result: %s, %v", mustMarshalJSON(result), err)
			}
			if transform.journalFinishRequested || transform.journalTerminalReady() {
				t.Fatal("failed batch completed the turn")
			}
			items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
			if err != nil || len(items) != 0 {
				t.Fatalf("failed batch leaked mutations: %+v, %v", items, err)
			}
			item = maps.Clone(item)
			item["call_id"] = mustTestJSON(t, "corrected")
			item["arguments"] = mustTestJSON(t, `{"op":"finish","journal":[{"op":"add","text":"Corrected"}]}`)
			if _, err := transform.executeJournalCall(item); err != nil || !transform.journalFinishRequested {
				t.Fatalf("correction failed: %v", err)
			}
		})
	}
}

func TestJournalFinishReportsUnavailableDelivery(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	transform.journalAvailable = false
	var call map[string]json.RawMessage
	if err := json.Unmarshal(mustTestJSON(t, journalFinishCall(`{"op":"finish"}`)), &call); err != nil {
		t.Fatal(err)
	}
	result, err := transform.executeJournalCall(call)
	if err != nil || !strings.Contains(jsonString(result, "output"), "journal terminal delivery unavailable") {
		t.Fatalf("wrong finish error: %s, %v", mustMarshalJSON(result), err)
	}
}

func requestJournalFinish(t *testing.T, transform *mekugiResponseTransform) {
	t.Helper()
	var call map[string]json.RawMessage
	if err := json.Unmarshal(mustTestJSON(t, journalFinishCall(`{"op":"finish"}`)), &call); err != nil {
		t.Fatal(err)
	}
	if _, err := transform.executeJournalCall(call); err != nil || !transform.journalTerminalReady() {
		t.Fatalf("journal finish rejected: %v", err)
	}
}

func TestProviderAnswerDoesNotSubstituteForJournalFinish(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/main", true: "/child"}[child], func(t *testing.T) {
				transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
				transform.subagentTurn = child
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "seed",
					[]journalMutation{{Op: "add", Text: new("Pending report")}}); err != nil {
					t.Fatal(err)
				}
				answer := map[string]any{"type": "message", "id": "unexpected-answer", "role": "assistant", "phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Provider text stays intact."}}}
				response := map[string]any{"id": "unexpected", "status": "completed", "output": []any{answer}}
				var output []byte
				if stream {
					original := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": answer})
					first, err := transform.TransformSSE(original)
					if err != nil {
						t.Fatal(err)
					}
					last, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response}))
					if err != nil {
						t.Fatal(err)
					}
					events := append(first, last...)
					output = bytes.Join(events, nil)
					if !bytes.Contains(output, original) {
						t.Fatal("provider event was filtered or rewritten")
					}
					for _, event := range events {
						transform.Delivered(event)
					}
				} else {
					var err error
					output, err = transform.TransformJSON(mustTestJSON(t, response))
					if err != nil {
						t.Fatal(err)
					}
					transform.Delivered(output)
				}
				transform.ReleaseDelivery()
				if !bytes.Contains(output, mustTestJSON(t, answer)) || bytes.Contains(output, []byte("Journal flush")) ||
					bytes.Contains(output, []byte("Journal saved:")) || transform.journalTerminalReady() {
					t.Fatalf("provider answer became a journal finish: %s", output)
				}
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
				if err != nil || len(items) != 1 || items[0].Flushed {
					t.Fatalf("provider answer consumed pending report: %+v, %v", items, err)
				}
			})
		}
	}
}

func TestJournalContinuationPreservesProviderOutput(t *testing.T) {
	for _, mode := range []string{"json", "sse-full", "sse-empty", "sse-absent", "sse-snapshot-only"} {
		t.Run(mode, func(t *testing.T) {
			stream := mode != "json"
			snapshot := strings.TrimPrefix(mode, "sse-")
			if !stream {
				snapshot = "full"
			}
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			call := map[string]any{"type": "function_call", "id": "list-item", "call_id": "list-call", "name": "journal", "arguments": `{"op":"list"}`, "status": "completed"}
			message := map[string]any{
				"type": "message", "id": "unexpected-message", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Unexpected provider text must remain visible."}},
			}
			provider := &serverFakeProvider{results: []serverForwardResult{
				{response: journalFinishResponse(t, stream, "completed", snapshot, call, message)},
				{response: journalFinishResponse(t, stream, "completed", snapshot, journalFinishCall(`{"op":"finish"}`))},
			}}
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil, nil); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 2 {
				t.Fatalf("provider requests = %d, want 2", len(provider.forwarded))
			}
			count := 0
			for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
				if jsonString(item, "id") == "unexpected-message" {
					count++
					if !bytes.Equal(mustMarshalJSON(item), mustMarshalJSON(message)) {
						t.Fatalf("provider message changed: %s", mustMarshalJSON(item))
					}
				}
			}
			if count != 1 {
				t.Fatalf("terminal retained %d provider messages, want 1: %s", count, output.Bytes())
			}
			if stream {
				completed := 0
				for line := range strings.SplitSeq(output.String(), "\n") {
					data, ok := strings.CutPrefix(line, "data: ")
					if !ok {
						continue
					}
					var event struct {
						Type string                     `json:"type"`
						Item map[string]json.RawMessage `json:"item"`
					}
					if json.Unmarshal([]byte(data), &event) == nil && event.Type == "response.output_item.done" && jsonString(event.Item, "id") == "unexpected-message" {
						completed++
					}
				}
				if completed != 1 {
					t.Fatalf("stream emitted %d completed provider messages, want 1", completed)
				}
			}
		})
	}
}
