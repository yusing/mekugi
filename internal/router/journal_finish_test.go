package router

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func assertJournalFinishRejected(t *testing.T, stream bool, wire []byte, callID, hostCallID string, wantIDs []string) {
	t.Helper()
	var resultCount int
	for _, item := range journalFinishClientOutput(t, stream, wire) {
		if journalResultCallID(item) != callID {
			continue
		}
		resultCount++
		var outcome struct {
			OK         bool     `json:"ok"`
			Error      string   `json:"error"`
			JournalIDs []string `json:"journal_ids"`
		}
		if err := json.Unmarshal([]byte(jsonString(item, "output")), &outcome); err != nil {
			t.Fatalf("decode rejected finish result %s: %v", mustMarshalJSON(item), err)
		}
		if outcome.OK || outcome.Error != journalFinishHostCallsError || !slices.Equal(outcome.JournalIDs, wantIDs) {
			t.Fatalf("finish result = %+v, want ok=false, retry guidance and IDs %v", outcome, wantIDs)
		}
	}
	if resultCount != 1 {
		t.Fatalf("finish results = %d, want exactly 1: %s", resultCount, wire)
	}
	if !stream {
		return
	}
	finishEvents, hostEvents, finishIndex, terminalIndex := 0, 0, -1, -1
	for index, payload := range finalAnswerTestPayloads(string(wire)) {
		var event struct {
			Type string                     `json:"type"`
			Item map[string]json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "response.output_item.done":
			if journalResultCallID(event.Item) == callID {
				finishEvents++
				finishIndex = index
				var outcome struct {
					OK    bool   `json:"ok"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal([]byte(jsonString(event.Item, "output")), &outcome); err != nil || outcome.OK || outcome.Error != journalFinishHostCallsError {
					t.Fatalf("SSE finish event result = %+v, err=%v", outcome, err)
				}
			}
			if jsonString(event.Item, "type") == "function_call" && jsonString(event.Item, "call_id") == hostCallID {
				hostEvents = index + 1 // Zero is a valid event index.
			}
		case "response.completed":
			if terminalIndex >= 0 {
				t.Fatal("duplicate terminal event")
			}
			terminalIndex = index
		}
	}
	if finishEvents != 1 || terminalIndex < 0 {
		t.Fatalf("SSE finish item-done events=%d terminal=%d; want one result before one terminal", finishEvents, terminalIndex)
	}
	if hostEvents != 0 && hostEvents-1 >= terminalIndex {
		t.Fatal("host call appeared after terminal event")
	}
	if hostEvents != 0 && finishIndex <= hostEvents-1 {
		t.Fatal("finish result was emitted before the host call was classified")
	}
}

func assertHostCallUnchanged(t *testing.T, stream bool, wire []byte, callID string, want map[string]any) {
	t.Helper()
	var found []map[string]json.RawMessage
	for _, item := range journalFinishClientOutput(t, stream, wire) {
		if jsonString(item, "type") == "function_call" && jsonString(item, "call_id") == callID {
			found = append(found, item)
		}
	}
	if len(found) != 1 || !bytes.Equal(mustMarshalJSON(found[0]), mustTestJSON(t, want)) {
		t.Fatalf("host call changed or duplicated: got %s, want %s", mustMarshalJSON(found), mustTestJSON(t, want))
	}
}

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
	t.Parallel()
	for _, mode := range []string{"json", "sse-full", "sse-empty", "sse-absent", "sse-snapshot-only"} {
		for _, mutationMode := range []string{"existing", "batched", "sibling"} {
			batched := mutationMode == "batched"
			name := mode + "/" + mutationMode
			t.Run(name, func(t *testing.T) {
				stream := mode != "json"
				snapshot := strings.TrimPrefix(mode, "sse-")
				if !stream {
					snapshot = "full"
				}
				proxy := newManagedMekugiProxy(t)
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
				} else if mutationMode == "existing" {
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "seed", []journalMutation{{Op: "add", Text: new("Completed the assigned milestone")}}); err != nil {
						t.Fatal(err)
					}
				}
				calls := []any{journalFinishCall(arguments)}
				if mutationMode == "sibling" {
					seed := map[string]any{"type": "function_call", "id": "seed-item", "call_id": "seed-call", "name": "journal", "arguments": `{"op":"add","text":"Completed the assigned milestone"}`, "status": "completed"}
					calls = append([]any{seed}, calls...)
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, stream, "completed", snapshot, calls...)}}}
				request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 {
					t.Fatalf("finish issued %d provider requests; want 1", len(provider.forwarded))
				}
				if !strings.Contains(output.String(), "Journal flush") || !strings.Contains(output.String(), "Completed the assigned milestone") {
					t.Fatalf("missing journal flush: %s", output.Bytes())
				}
				assertJournalFinishOrder(t, stream, output.Bytes())
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
						if batched && (len(result.JournalIDs) != 1 || result.JournalIDs[0] != "amber") {
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
				proxy := newManagedMekugiProxy(t)
				workspace := t.TempDir()
				arguments := `{"op":"finish"}`
				invalid := strings.HasPrefix(scenario, "invalid-")
				if invalid {
					arguments = map[string]string{"invalid-id": `{"op":"finish","id":"amber"}`, "invalid-text": `{"op":"finish","text":"not allowed"}`, "invalid-agent": `{"op":"finish","agent":"/root"}`, "invalid-report-now": `{"op":"finish","report_now":true}`}[scenario]
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
				err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil)
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
				if scenario == "mixed" {
					assertJournalFinishRejected(t, stream, output.Bytes(), "finish-call", "lookup-call", nil)
					assertHostCallUnchanged(t, stream, output.Bytes(), "lookup-call", pending)
				}
			})
		}
	}
}

func TestJournalFinishWithHostCallReturnsOneCorrectableResult(t *testing.T) {
	for _, scenario := range []struct {
		name, snapshot string
		stream         bool
		finishFirst    bool
	}{
		{name: "json-full-finish-before-host", snapshot: "full", finishFirst: true},
		{name: "sse-full-finish-after-host", snapshot: "full", stream: true},
		{name: "sse-snapshot-only-host", snapshot: "snapshot-only", stream: true, finishFirst: true},
		{name: "sse-empty-terminal-after-calls", snapshot: "empty", stream: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.journals = newJournalStore()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			workspace := t.TempDir()
			finish := journalFinishCall(`{"op":"finish","journal":[{"op":"add","text":"Milestone survives retry"}]}`)
			hostCall := map[string]any{
				"type": "function_call", "id": "host-item", "call_id": "host-call", "name": "lookup",
				"arguments": `{"path":"README.md","max":1}`, "status": "completed",
			}
			calls := []any{hostCall, finish}
			if scenario.finishFirst {
				calls = []any{finish, hostCall}
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, scenario.stream, "completed", scenario.snapshot, calls...)}}}
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = scenario.stream })
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "mixed-host-turn", provider, &output, NewCriticalErrors(), proxy, nil); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || strings.Contains(output.String(), "Journal flush") {
				t.Fatalf("mixed finish completed or retried before host work: requests=%d output=%s", len(provider.forwarded), output.Bytes())
			}
			assertJournalFinishRejected(t, scenario.stream, output.Bytes(), "finish-call", "host-call", []string{"amber"})
			assertHostCallUnchanged(t, scenario.stream, output.Bytes(), "host-call", hostCall)
			items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
			if err != nil || len(items) != 1 || items[0].ID != "amber" || items[0].Text != "Milestone survives retry" || items[0].Reported || items[0].Flushed {
				t.Fatalf("batched mutation did not survive rejected finish: %+v, err=%v", items, err)
			}
		})
	}
}

func TestJournalFinishHostRetrySucceedsOnNextTurn(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	proxy.journals = newJournalStore()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	workspace := t.TempDir()
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	hostCall := map[string]any{
		"type": "function_call", "id": "host-item", "call_id": "host-call", "name": "lookup",
		"arguments": `{"path":"README.md","max":1}`, "status": "completed",
	}
	firstResponse := journalFinishResponse(t, false, "completed", "full",
		journalFinishCall(`{"op":"finish","journal":[{"op":"add","text":"Milestone survives retry"}]}`), hostCall)
	firstProvider := &serverFakeProvider{results: []serverForwardResult{{response: firstResponse}}}
	var firstOutput bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "first-turn", firstProvider, &firstOutput, NewCriticalErrors(), proxy, nil); err != nil {
		t.Fatal(err)
	}
	if len(firstProvider.forwarded) != 1 {
		t.Fatalf("host result handling required an automatic continuation: %d", len(firstProvider.forwarded))
	}
	assertJournalFinishRejected(t, false, firstOutput.Bytes(), "finish-call", "host-call", []string{"amber"})
	assertHostCallUnchanged(t, false, firstOutput.Bytes(), "host-call", hostCall)

	input := serverRequest(t, nil)
	var priorInput []any
	if err := json.Unmarshal(input.fields["input"], &priorInput); err != nil {
		t.Fatal(err)
	}
	for _, item := range journalFinishClientOutput(t, false, firstOutput.Bytes()) {
		priorInput = append(priorInput, item)
	}
	priorInput = append(priorInput, map[string]any{
		"type": "function_call_output", "call_id": "host-call", "output": "Host lookup result inspected: README.md contains the expected guidance.",
	})
	input.setInput(mustTestJSON(t, priorInput))
	retryFinish := map[string]any{
		"type": "function_call", "id": "retry-finish-item", "call_id": "retry-finish-call",
		"name": "journal", "namespace": "functions", "arguments": `{"op":"finish"}`, "status": "completed",
	}
	secondProvider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, false, "completed", "full", retryFinish)}}}
	var secondOutput bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), input, headers, "retry-turn", secondProvider, &secondOutput, NewCriticalErrors(), proxy, nil); err != nil {
		t.Fatal(err)
	}
	if len(secondProvider.forwarded) != 1 {
		t.Fatalf("finish retry issued %d provider requests, want one", len(secondProvider.forwarded))
	}
	for _, inspected := range []string{journalFinishHostCallsError, "Host lookup result inspected: README.md contains the expected guidance."} {
		if !bytes.Contains(secondProvider.forwarded[0], []byte(inspected)) {
			t.Fatalf("retry omitted inspected result %q from provider input: %s", inspected, secondProvider.forwarded[0])
		}
	}
	if !strings.Contains(secondOutput.String(), "Journal flush") || strings.Contains(secondOutput.String(), journalFinishHostCallsError) {
		t.Fatalf("host-free retry did not finish the retained milestone: %s", secondOutput.Bytes())
	}
	foundRetryResult := false
	for _, item := range journalFinishClientOutput(t, false, secondOutput.Bytes()) {
		if journalResultCallID(item) != "retry-finish-call" {
			continue
		}
		foundRetryResult = true
		var outcome struct {
			OK              bool `json:"ok"`
			FinishRequested bool `json:"finish_requested"`
		}
		if err := json.Unmarshal([]byte(jsonString(item, "output")), &outcome); err != nil || !outcome.OK || !outcome.FinishRequested {
			t.Fatalf("successful retry result = %+v, err=%v", outcome, err)
		}
	}
	if !foundRetryResult {
		t.Fatal("retry response omitted its successful finish result")
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 1 || items[0].ID != "amber" || !items[0].Reported || !items[0].Flushed {
		t.Fatalf("host-free retry did not durably flush original mutation: %+v, err=%v", items, err)
	}
}

func TestJournalFinishReplayDoesNotFinishLaterCRUD(t *testing.T) {
	workspace, storeDirectory := t.TempDir(), t.TempDir()
	proxy := newManagedMekugiProxy(t)
	var err error
	proxy.replayStore, err = openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: journalFinishResponse(t, false, "completed", "full", journalFinishCall(`{"op":"finish","journal":[{"op":"add","text":"First task completed"}]}`))}}}
	var completed bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "original-session", provider, &completed, NewCriticalErrors(), proxy, nil); err != nil {
		t.Fatal(err)
	}
	history := journalFinishClientOutput(t, false, completed.Bytes())
	resumed := newManagedMekugiProxy(t)
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
	if err := executeRequest(t.Context(), t.Context(), request, headers, "fresh-session", provider, &output, NewCriticalErrors(), resumed, nil); err != nil {
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
		`[{"op":"edit","id":"bloom","text":"missing"}]`,
		`[{"op":"add","text":"must roll back"},{"op":"edit","id":"bloom","text":"missing"}]`,
	} {
		t.Run(batch, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t)
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
	transform, _, _, _ := newMekugiTestTransform(t)
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

func TestNaturalProviderAnswerBecomesJournalTerminalResult(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/main", true: "/child"}[child], func(t *testing.T) {
				transform, proxy, _, workspace := newMekugiTestTransform(t)
				transform.subagentTurn = child
				transform.journalQuestion = "How did the task go?"
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "seed",
					[]journalMutation{{Op: "add", Text: new("Pending report")}}); err != nil {
					t.Fatal(err)
				}
				answer := map[string]any{"type": "message", "id": "natural-answer", "role": "assistant", "phase": "final_answer", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": "Provider answer is captured."}}}
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
				if bytes.Contains(output, []byte(`"id":"natural-answer"`)) || !bytes.Contains(output, []byte("Provider answer is captured.")) {
					t.Fatalf("provider answer was not exclusively rendered through the journal: %s", output)
				}
				if child {
					if !bytes.Contains(output, []byte("Journal result")) {
						t.Fatalf("child completion omitted its journal result: %s", output)
					}
				} else if !bytes.Contains(output, []byte("Journal flush")) {
					t.Fatalf("main completion omitted its journal flush: %s", output)
				}
				if !bytes.Contains(output, []byte("**Question:**")) || !bytes.Contains(output, []byte("How did the task go?")) ||
					!bytes.Contains(output, []byte("**Answer:**")) {
					t.Fatalf("natural answer lost its question association: %s", output)
				}
				items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, transform.shellThreadID)
				if err != nil || len(items) != 2 || items[0].Text != "Pending report" || items[1].Text != "Provider answer is captured." ||
					items[1].Question != "How did the task go?" {
					t.Fatalf("natural answer was not stored alongside the pending report: %+v, %v", items, err)
				}
			})
		}
	}
}

func TestJournalListContinuationCapturesNaturalAnswer(t *testing.T) {
	for _, mode := range []string{"json", "sse-full", "sse-empty", "sse-absent", "sse-snapshot-only"} {
		t.Run(mode, func(t *testing.T) {
			stream := mode != "json"
			snapshot := strings.TrimPrefix(mode, "sse-")
			if !stream {
				snapshot = "full"
			}
			proxy := newManagedMekugiProxy(t)
			call := map[string]any{"type": "function_call", "id": "list-item", "call_id": "list-call", "name": "journal", "arguments": `{"op":"list"}`, "status": "completed"}
			progress := map[string]any{"type": "message", "id": "intermediate-message", "role": "assistant", "phase": "commentary", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Provider interim progress."}}}
			message := map[string]any{
				"type": "message", "id": "unexpected-message", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Natural provider final answer."}},
			}
			provider := &serverFakeProvider{results: []serverForwardResult{
				{response: journalFinishResponse(t, stream, "completed", snapshot, call, progress)},
				{response: journalFinishResponse(t, stream, "completed", snapshot, message)},
			}}
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil}), "session", provider, &output, NewCriticalErrors(), proxy, nil); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 2 {
				t.Fatalf("provider requests = %d, want 2", len(provider.forwarded))
			}
			if !bytes.Contains(provider.forwarded[1], []byte("function_call_output")) ||
				!bytes.Contains(provider.forwarded[1], []byte("list-call")) ||
				!bytes.Contains(provider.forwarded[1], []byte("intermediate-message")) {
				t.Fatalf("journal result or non-answer provider history was lost on continuation: %s", provider.forwarded[1])
			}
			if bytes.Contains(output.Bytes(), []byte(`"id":"unexpected-message"`)) ||
				!bytes.Contains(output.Bytes(), []byte("Provider interim progress.")) {
				t.Fatalf("natural final answer was not replaced or interim progress was lost: %s", output.Bytes())
			}
			var flushes int
			for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
				text := commentaryMessageText(item)
				if strings.Contains(text, "Journal flush") {
					flushes++
					if !strings.Contains(text, "**Question:**") || !strings.Contains(text, "task") ||
						!strings.Contains(text, "**Answer:**") || !strings.Contains(text, "Natural provider final answer.") {
						t.Fatalf("natural final answer lost Q/A rendering: %s", text)
					}
				}
			}
			if flushes != 1 {
				t.Fatalf("terminal flushes = %d, want one: %s", flushes, output.Bytes())
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
				if completed != 0 {
					t.Fatalf("stream emitted %d raw provider final answers, want 0", completed)
				}
			}
		})
	}
}

func TestJournalEmptyFinishPersistsTokenMetricsSilently(t *testing.T) {
	for _, scenario := range []string{"complete", "prior-gap", "missing-current"} {
		for _, stream := range []bool{false, true} {
			t.Run(scenario+"/"+map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())

				// A previous accepted request with missing usage permanently invalidates totals.
				proxy := newManagedMekugiProxy(t)
				if scenario == "prior-gap" {
					proxy.usage.observation("thread-1", "", "gpt-6-astra", "").finish()
				}
				call := journalFinishCall(`{"op":"finish"}`)
				body := map[string]any{
					"id": "empty-finish", "status": "completed", "output": []any{call},
					"usage": map[string]any{
						"input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 12},
						"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
					},
				}
				if scenario == "missing-current" {
					delete(body, "usage")
					proxy.usage.observation("thread-1", "", "gpt-6-astra", "").observe(tokenCounts{InputTokens: 100})
				}
				response := serverHTTPResponse(string(mustTestJSON(t, body)))
				if stream {
					response = serverHTTPResponse(finalAnswerTestWire([][]byte{
						mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}),
						mustTestJSON(t, map[string]any{"type": "response.completed", "response": body}),
					}))
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, &output, nil, proxy, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 {
					t.Fatalf("finish issued %d provider requests; want 1", len(provider.forwarded))
				}
				if strings.Contains(output.String(), "Journal flush") {
					t.Fatal("empty journal emitted a flush")
				}
				if strings.Contains(output.String(), "Router session usage") || strings.Contains(output.String(), "Usage incomplete") {
					t.Fatalf("empty finish exposed metrics in completion commentary: %s", output.Bytes())
				}
				paths := proxy.tokenMetricPaths()
				if len(paths) != 1 {
					t.Fatalf("empty finish metric paths = %q", paths)
				}
				markdown, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "complete" {
					if !strings.Contains(string(markdown), "20 (60.0%)") {
						t.Fatalf("incorrect saved metrics: %s", markdown)
					}
				} else if !strings.Contains(string(markdown), "Usage incomplete") {
					t.Fatalf("usage gap missing from saved metrics: %s", markdown)
				}
			})
		}
	}
}

func assertJournalFinishOrder(t *testing.T, stream bool, wire []byte) {
	t.Helper()
	check := func(items []map[string]json.RawMessage) {
		t.Helper()
		flush := -1
		for i, item := range items {
			text := commentaryMessageText(item)
			if strings.Contains(text, "Router session usage") {
				t.Fatalf("completion exposed token metrics as commentary: %s", text)
			}
			if strings.HasPrefix(text, "Journal flush ") {
				if flush >= 0 {
					t.Fatal("duplicate final journal flush")
				}
				if jsonString(item, "phase") != "final_answer" {
					t.Fatal("journal flush is not a final answer")
				}
				flush = i
			}
		}
		if flush < 0 || flush != len(items)-1 {
			t.Fatalf("want one final journal flush last; flush=%d items=%s", flush, mustMarshalJSON(items))
		}
	}
	output := journalFinishClientOutput(t, stream, wire)
	check(output)
	if !stream {
		return
	}
	var done []map[string]json.RawMessage
	completed := false
	for _, payload := range finalAnswerTestPayloads(string(wire)) {
		var event struct {
			Type string                     `json:"type"`
			Item map[string]json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "response.output_item.done":
			if completed {
				t.Fatal("item emitted after terminal event")
			}
			done = append(done, event.Item)
		case "response.completed":
			if completed {
				t.Fatal("duplicate terminal event")
			}
			completed = true
			check(done)
		}
	}
	if !completed {
		t.Fatal("missing terminal event")
	}
	if jsonString(done[len(done)-1], "id") != jsonString(output[len(output)-1], "id") {
		t.Fatal("stream and terminal snapshot disagree on final journal identity")
	}
}
