package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/responses"
)

func mentorTestHeaders(t *testing.T, threadID string) http.Header {
	t.Helper()
	metadata, err := json.Marshal(codexTurnMetadata{RequestKind: "turn", SubagentKind: threadSpawnSubagentKind})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set(openAISubagentHeader, threadSpawnSubagent)
	headers.Set(codexTurnMetadataHeader, string(metadata))
	if threadID != "" {
		headers.Set(threadIDHeader, threadID)
	}
	return headers
}

func mentorTestRequest(t *testing.T, model string) parsedResponsesRequest {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": model,
		"reasoning": map[string]any{
			"effort":  "medium",
			"summary": "auto",
		},
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "keep exact history"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mentorTestItems(t *testing.T, itemTypes ...string) []json.RawMessage {
	t.Helper()
	items := make([]json.RawMessage, 0, len(itemTypes))
	for index, itemType := range itemTypes {
		item := map[string]any{"type": itemType, "id": fmt.Sprintf("item-%d", index)}
		if itemType == "message" {
			item["role"] = "assistant"
		}
		items = append(items, mustTestJSON(t, item))
	}
	return items
}

func TestMentorHandoffRecognizesMainAndCanonicalThreadSpawn(t *testing.T) {
	mentor := newMentorHandoff(true, true)
	tests := []struct {
		name, model, wantModel string
		headers                http.Header
		want                   bool
		wantErr                string
	}{
		{name: "thread spawn", model: "gpt-5.6-luna", headers: mentorTestHeaders(t, "child"), want: true, wantModel: "gpt-6-sol high"},
		{name: "second eligible model", model: "gpt-5.6-terra", headers: mentorTestHeaders(t, "child-terra"), want: true, wantModel: "gpt-6-sol high"},
		{name: "sol successor", model: "gpt-6-sol", headers: mentorTestHeaders(t, "child-sol"), want: true, wantModel: "gpt-6-astra low"},
		{name: "luna successor", model: "gpt-6-luna", headers: mentorTestHeaders(t, "child-luna"), want: true, wantModel: "gpt-6-sol high"},
		{name: "ordinary session", model: "gpt-5.6-luna", headers: http.Header{}},
		{name: "ordinary fork metadata", model: "gpt-5.6-luna", headers: serverMetadataHeaders(t, "turn", nil), want: true, wantModel: "gpt-6-astra medium"},
		{name: "astra unchanged", model: "gpt-6-astra", headers: mentorTestHeaders(t, "leader")},
		{name: "unknown lower model", model: "gpt-test", headers: mentorTestHeaders(t, "unknown")},
		{name: "marker without metadata", model: "gpt-5.6-luna", headers: http.Header{openAISubagentHeader: []string{threadSpawnSubagent}}, wantErr: "canonical thread-spawn metadata"},
		{name: "marker without thread", model: "gpt-5.6-luna", headers: mentorTestHeaders(t, ""), wantErr: "Codex thread ID"},
		{name: "duplicate marker", model: "gpt-5.6-luna", headers: http.Header{openAISubagentHeader: []string{threadSpawnSubagent, threadSpawnSubagent}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := mentorTestRequest(t, test.model)
			metadata, valid := decodeCodexTurnMetadata(test.headers)
			handoff, err := mentor.prepare(test.headers, metadata, valid, &request)
			if test.wantErr != "" {
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
					t.Fatalf("error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (handoff != nil) != test.want {
				t.Fatalf("handoff = %v, want active %t", handoff != nil, test.want)
			}
			if !test.want {
				if got := request.model(); got != test.model {
					t.Fatalf("model = %q, want unchanged %q", got, test.model)
				}
				return
			}
			if got := request.modelDescription(); got != test.wantModel {
				t.Fatalf("leader request = %q, want %q", got, test.wantModel)
			}
			var reasoning map[string]json.RawMessage
			if err := json.Unmarshal(request.fields["reasoning"], &reasoning); err != nil {
				t.Fatal(err)
			}
			if string(reasoning["summary"]) != `"auto"` {
				t.Fatalf("reasoning siblings = %s", request.fields["reasoning"])
			}
		})
	}
}

func TestMentorHandoffIndependentToggles(t *testing.T) {
	flags := newRouterFlags(io.Discard)
	if !*flags.mainMentorHandoffEnabled || !*flags.mentorHandoffEnabled {
		t.Fatal("main and subagent mentor must default on")
	}
	for _, mainEnabled := range []bool{false, true} {
		for _, subagentEnabled := range []bool{false, true} {
			for _, child := range []bool{false, true} {
				headers := mentorTestHeaders(t, "thread")
				want := subagentEnabled
				if !child {
					headers.Del(openAISubagentHeader)
					headers.Set(codexTurnMetadataHeader, `{"request_kind":"turn"}`)
					want = mainEnabled
				}
				request := mentorTestRequest(t, "gpt-5.6-sol")
				metadata, valid := decodeCodexTurnMetadata(headers)
				mentor := newMentorHandoff(mainEnabled, subagentEnabled)
				handoff, err := mentor.prepare(headers, metadata, valid, &request)
				if err != nil || (handoff != nil) != want {
					t.Fatalf("main=%t subagent=%t child=%t: handoff=%v err=%v", mainEnabled, subagentEnabled, child, handoff, err)
				}
				wantModel := "gpt-5.6-sol medium"
				if want {
					wantModel = "gpt-6-astra low"
				}
				if request.modelDescription() != wantModel {
					t.Fatalf("request=%q want=%q", request.modelDescription(), wantModel)
				}
				astra := mentorTestRequest(t, "gpt-6-astra")
				if handoff, err := mentor.prepare(headers, metadata, valid, &astra); err != nil || handoff != nil || astra.model() != "gpt-6-astra" {
					t.Fatal("configured Astra must never hand off to Sol")
				}
			}
		}
	}
}

func TestMentorHandoffAstraMapping(t *testing.T) {
	for _, test := range []struct{ effort, want string }{
		{"low", "low"}, {"medium", "low"}, {"high", "medium"},
		{"xhigh", "high"}, {"max", "xhigh"}, {"ultra", "xhigh"}, {"", "low"},
	} {
		t.Run(test.effort, func(t *testing.T) {
			for _, main := range []bool{false, true} {
				mentor := newMentorHandoff(true, true)
				headers := mentorTestHeaders(t, "thread")
				if main {
					headers.Del(openAISubagentHeader)
					headers.Set(codexTurnMetadataHeader, `{"request_kind":"turn"}`)
				}
				metadata, valid := decodeCodexTurnMetadata(headers)
				request := mentorTestRequest(t, "gpt-5.6")
				if test.effort == "" {
					delete(request.fields, "reasoning")
				} else if err := request.setModelAndReasoningEffort("gpt-5.6", test.effort); err != nil {
					t.Fatal(err)
				}
				handoff, err := mentor.prepare(headers, metadata, valid, &request)
				if err != nil || handoff == nil || request.modelDescription() != "gpt-6-astra "+test.want {
					t.Fatalf("main=%t: request=%q handoff=%v err=%v", main, request.modelDescription(), handoff, err)
				}
				handoff.record(requestCompletion{outcome: requestOutcomeCompleted, terminal: responseTerminalCompleted, usage: tokenCounts{InputTokens: mentorInputTokenLimit}, usageObserved: true})
				request = mentorTestRequest(t, "gpt-5.6")
				handoff, err = mentor.prepare(headers, metadata, valid, &request)
				if err != nil || handoff != nil || request.modelDescription() != "gpt-5.6 medium" {
					t.Fatalf("post-handoff main=%t: request=%q handoff=%v err=%v", main, request.modelDescription(), handoff, err)
				}
			}
		})
	}
}

func TestMentorHandoffMainBoundary(t *testing.T) {
	for _, test := range []struct {
		name, model, metadata, marker, wantModel string
		want                                     bool
	}{
		{"main luna", "gpt-5.6-luna", `{"request_kind":"turn"}`, "", "gpt-6-astra medium", true},
		{"main gpt-6 luna", "gpt-6-luna", `{"request_kind":"turn"}`, "", "gpt-6-astra medium", true},
		{"main gpt-6 sol", "gpt-6-sol", `{"request_kind":"turn"}`, "", "gpt-6-astra low", true},
		{"main terra", "gpt-5.6-terra", `{"request_kind":"turn"}`, "", "gpt-6-sol high", true},
		{"main astra unchanged", "gpt-6-astra", `{"request_kind":"turn"}`, "", "gpt-6-astra medium", false},
		{"main prewarm", "gpt-5.6-luna", `{"request_kind":"prewarm"}`, "", "gpt-5.6-luna medium", false},
		{"main compaction", "gpt-5.6-luna", `{"request_kind":"compaction"}`, "", "gpt-5.6-luna medium", true},
		{"missing request kind", "gpt-5.6-luna", `{}`, "", "gpt-5.6-luna medium", false},
		{"missing metadata", "gpt-5.6-luna", "", "", "gpt-5.6-luna medium", false},
		{"invalid metadata", "gpt-5.6-luna", "{", "", "gpt-5.6-luna medium", false},
		{"unmarked child", "gpt-5.6-luna", `{"subagent_kind":"thread_spawn"}`, "", "gpt-5.6-luna medium", false},
		{"other subagent", "gpt-5.6-luna", `{"request_kind":"turn"}`, "review", "gpt-5.6-luna medium", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(threadIDHeader, "main")
			if test.metadata != "" {
				headers.Set(codexTurnMetadataHeader, test.metadata)
			}
			if test.marker != "" {
				headers.Set(openAISubagentHeader, test.marker)
			}
			request := mentorTestRequest(t, test.model)
			metadata, valid := decodeCodexTurnMetadata(headers)
			handoff, err := newMentorHandoff(true, true).prepare(headers, metadata, valid, &request)
			if err != nil || (handoff != nil) != test.want {
				t.Fatalf("handoff=%v err=%v, want active=%t", handoff, err, test.want)
			}
			if request.modelDescription() != test.wantModel {
				t.Fatalf("request=%q, want %q", request.modelDescription(), test.wantModel)
			}
		})
	}
}

func TestMentorHandoffCompletesAtEachBound(t *testing.T) {
	tests := []struct {
		name       string
		responses  [][]json.RawMessage
		inputUsage []uint64
	}{
		{name: "tool calls", responses: [][]json.RawMessage{mentorTestItems(t, "custom_tool_call", "function_call"), mentorTestItems(t, "custom_tool_call"), nil}, inputUsage: []uint64{10_000, 10_000, 10_000}},
		{name: "messages", responses: [][]json.RawMessage{mentorTestItems(t, "message"), mentorTestItems(t, "message")}, inputUsage: []uint64{10_000, 10_000}},
		{name: "input tokens with overshoot", responses: [][]json.RawMessage{nil, nil, nil}, inputUsage: []uint64{30_000, 25_000, 50_001}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mentor := newMentorHandoff(true, true)
			headers := mentorTestHeaders(t, "child")
			metadata, valid := decodeCodexTurnMetadata(headers)
			for index, items := range test.responses {
				request := mentorTestRequest(t, "gpt-5.6-luna")
				handoff, err := mentor.prepare(headers, metadata, valid, &request)
				if err != nil {
					t.Fatal(err)
				}
				if handoff == nil {
					t.Fatalf("response %d was handed off early", index+1)
				}
				handoff.observation.observeItems(items)
				progress := handoff.record(requestCompletion{outcome: requestOutcomeCompleted, terminal: responseTerminalCompleted, usage: tokenCounts{InputTokens: test.inputUsage[index]}, usageObserved: true})
				if got, want := progress.complete, index == len(test.responses)-1; got != want {
					t.Fatalf("response %d complete = %t, want %t", index+1, got, want)
				}
				if test.name == "tool calls" && index == 1 && !progress.awaitingToolResult {
					t.Fatal("tool-call threshold did not retain the mentor for its result")
				}
				if test.name == "input tokens with overshoot" && progress.latestInputTokens != test.inputUsage[index] {
					t.Fatalf("response %d input tokens = %d, want latest usage %d", index+1, progress.latestInputTokens, test.inputUsage[index])
				}
			}
			request := mentorTestRequest(t, "gpt-5.6-luna")
			handoff, err := mentor.prepare(headers, metadata, valid, &request)
			if err != nil {
				t.Fatal(err)
			}
			if handoff != nil || request.modelDescription() != "gpt-5.6-luna medium" {
				t.Fatalf("post-handoff request = %q, active = %t", request.modelDescription(), handoff != nil)
			}
		})
	}
}

func TestMentorHandoffWaitsForCompletedToolResultResponse(t *testing.T) {
	mentor := newMentorHandoff(true, true)
	headers := mentorTestHeaders(t, "child")
	metadata, valid := decodeCodexTurnMetadata(headers)
	record := func(items []json.RawMessage, completed bool) mentorProgress {
		t.Helper()
		request := mentorTestRequest(t, "gpt-5.6-luna")
		handoff, err := mentor.prepare(headers, metadata, valid, &request)
		if err != nil {
			t.Fatal(err)
		}
		if handoff == nil {
			t.Fatal("mentor handed off before a completed result-consuming response")
		}
		handoff.observation.observeItems(items)
		result := requestCompletion{usage: tokenCounts{InputTokens: 1_000}, usageObserved: true}
		if completed {
			result.outcome, result.terminal = requestOutcomeCompleted, responseTerminalCompleted
		}
		return handoff.record(result)
	}

	if progress := record(mentorTestItems(t, "custom_tool_call", "custom_tool_call", "custom_tool_call"), true); !progress.awaitingToolResult || progress.complete {
		t.Fatalf("tool threshold progress = %#v", progress)
	}
	if progress := record(nil, false); !progress.awaitingToolResult || progress.complete {
		t.Fatalf("failed result-consuming response progress = %#v", progress)
	}
	if progress := record(nil, true); progress.awaitingToolResult || !progress.complete {
		t.Fatalf("completed result-consuming response progress = %#v", progress)
	}
}

func TestMentorResponseObservationDoesNotDoubleCountStreamingItems(t *testing.T) {
	item := mentorTestItems(t, "custom_tool_call")[0]
	completed := mustTestJSON(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"output": []json.RawMessage{item}},
	})
	done := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})
	var observation mentorResponseObservation
	hooks := &responseHooks{}
	hooks.output = &observation
	if err := hooks.observe(done, true); err != nil {
		t.Fatal(err)
	}
	if err := hooks.observe(completed, true); err != nil {
		t.Fatal(err)
	}
	if observation.toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", observation.toolCalls)
	}
	if err := hooks.observe([]byte("[DONE]"), true); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteRequestMentorHandoffPreservesHistoryAndRestoresRequestedModel(t *testing.T) {
	response := func(itemTypes ...string) *http.Response {
		return serverHTTPResponse(string(mustTestJSON(t, map[string]any{
			"status": "completed",
			"output": mentorTestItems(t, itemTypes...),
			"usage":  map[string]any{"input_tokens": 10_000},
		})))
	}
	provider := &serverFakeProvider{results: []serverForwardResult{
		{response: response("custom_tool_call", "function_call")},
		{response: response("custom_tool_call")},
		{response: response("message")},
		{response: response("message")},
	}}
	mentor := newMentorHandoff(true, true)
	headers := mentorTestHeaders(t, "child")
	for range 4 {
		request := mentorTestRequest(t, "gpt-5.6-luna")
		if err := executeRequest(
			t.Context(), t.Context(), request, headers, "session", provider, io.Discard,
			nil, nil, mentor,
		); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.forwarded) != 4 {
		t.Fatalf("upstream requests = %d, want 4", len(provider.forwarded))
	}
	wantModels := []string{mentorLeaderModel, mentorLeaderModel, mentorLeaderModel, "gpt-5.6-luna"}
	for index, body := range provider.forwarded {
		request, err := parseResponsesRequest(body)
		if err != nil {
			t.Fatal(err)
		}
		if got := request.model(); got != wantModels[index] {
			t.Errorf("request %d model = %q, want %q", index+1, got, wantModels[index])
		}
		if !bytes.Contains(request.fields["input"], []byte("keep exact history")) {
			t.Errorf("request %d lost inherited input: %s", index+1, request.fields["input"])
		}
	}
}

func TestExecuteRequestMainNonTurnsDoNotConsumeMentorBudget(t *testing.T) {
	for _, kind := range []string{"prewarm", "compaction"} {
		t.Run(kind, func(t *testing.T) {
			provider := &serverFakeProvider{}
			for range 2 {
				provider.results = append(provider.results, serverForwardResult{
					response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
						"status": "completed",
						"output": mentorTestItems(t, "message", "message"),
						"usage":  map[string]any{"input_tokens": 55_000},
					}))),
				})
			}
			mentor := newMentorHandoff(true, false)
			for index, requestKind := range []string{kind, "turn"} {
				headers := serverMetadataHeaders(t, requestKind, nil)
				headers.Set(threadIDHeader, "main")
				request := mentorTestRequest(t, "gpt-5.6-sol")
				if err := executeRequest(t.Context(), t.Context(), request, headers, "main",
					provider, io.Discard, nil, nil, mentor); err != nil {
					t.Fatal(err)
				}
				if index == 0 && len(mentor.sessions) != 0 {
					t.Fatal("non-turn response started or consumed the main schedule")
				}
			}
			for index, want := range []string{"gpt-5.6-sol medium", "gpt-6-astra low"} {
				request, err := parseResponsesRequest(provider.forwarded[index])
				if err != nil || request.modelDescription() != want {
					t.Fatalf("request %d = %q, err=%v, want %q", index, request.modelDescription(), err, want)
				}
			}
		})
	}
}

func TestExecuteRequestCompactionRestartsMentorHandoff(t *testing.T) {
	for _, tc := range []struct {
		name          string
		headers       func(string) http.Header
		wantNextModel string
	}{
		{
			name: "main",
			headers: func(kind string) http.Header {
				headers := serverMetadataHeaders(t, kind, nil)
				headers.Set(threadIDHeader, "main")
				return headers
			},
			wantNextModel: "gpt-6-astra medium",
		},
		{
			name: "subagent",
			headers: func(kind string) http.Header {
				headers := mentorTestHeaders(t, "child")
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, codexTurnMetadata{
					RequestKind: responses.RequestKind(kind), SubagentKind: threadSpawnSubagentKind,
				})))
				return headers
			},
			wantNextModel: "gpt-6-sol high",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &serverFakeProvider{}
			for range 2 {
				provider.results = append(provider.results, serverForwardResult{
					response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
						"status": "completed",
						"output": mentorTestItems(t, "custom_tool_call"),
						"usage":  map[string]any{"input_tokens": 1_000},
					}))),
				})
			}
			mentor := newMentorHandoff(true, true)
			threadID := codexThreadID(tc.headers("turn"))
			mentor.sessions[threadID] = mentorSession{complete: true, messages: mentorMinMessages}

			for _, kind := range []string{"compaction", "turn"} {
				model := "gpt-5.6-luna"
				if kind == "compaction" {
					model = "gpt-6-astra"
				}
				request := mentorTestRequest(t, model)
				if err := executeRequest(t.Context(), t.Context(), request, tc.headers(kind), threadID,
					provider, io.Discard, nil, nil, mentor); err != nil {
					t.Fatal(err)
				}
			}

			for index, want := range []string{"gpt-6-astra medium", tc.wantNextModel} {
				request, err := parseResponsesRequest(provider.forwarded[index])
				if err != nil || request.modelDescription() != want {
					t.Fatalf("request %d = %q, err=%v, want %q", index, request.modelDescription(), err, want)
				}
			}
			if state := mentor.sessions[threadID]; state.complete || state.toolCalls != 1 {
				t.Fatalf("restarted schedule = %+v, want one active tool call", state)
			}
		})
	}
}

func TestExecuteRequestFailedCompactionPreservesCompletedHandoff(t *testing.T) {
	provider := &serverFakeProvider{results: []serverForwardResult{
		{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
			"status": "failed",
			"usage":  map[string]any{"input_tokens": mentorInputTokenLimit},
		})))},
		{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
			"status": "completed",
			"output": mentorTestItems(t, "message"),
			"usage":  map[string]any{"input_tokens": 1_000},
		})))},
	}}
	mentor := newMentorHandoff(true, false)
	mentor.sessions["main"] = mentorSession{complete: true, messages: mentorMinMessages}
	for _, kind := range []string{"compaction", "turn"} {
		headers := serverMetadataHeaders(t, kind, nil)
		headers.Set(threadIDHeader, "main")
		request := mentorTestRequest(t, "gpt-5.6-luna")
		if err := executeRequest(t.Context(), t.Context(), request, headers, "main",
			provider, io.Discard, nil, nil, mentor); err != nil {
			t.Fatal(err)
		}
	}
	for index, body := range provider.forwarded {
		request, err := parseResponsesRequest(body)
		if err != nil || request.modelDescription() != "gpt-5.6-luna medium" {
			t.Fatalf("request %d = %q, err=%v, want configured model", index, request.modelDescription(), err)
		}
	}
	if state := mentor.sessions["main"]; !state.complete || state.messages != mentorMinMessages {
		t.Fatalf("failed compaction changed schedule: %+v", state)
	}
}

func TestExecuteRequestSteeredCompactionPreservesCompletedHandoff(t *testing.T) {
	incomplete := mustTestJSON(t, map[string]any{
		"type": "response.incomplete",
		"response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "steered"},
			"usage":              map[string]any{"input_tokens": 1_000},
		},
	})
	streamed := serverHTTPResponse(finalAnswerTestWire([][]byte{incomplete}))
	streamed.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{
		{response: streamed},
		{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
			"status": "completed",
			"output": mentorTestItems(t, "message"),
			"usage":  map[string]any{"input_tokens": 1_000},
		})))},
	}}
	mentor := newMentorHandoff(true, false)
	mentor.sessions["main"] = mentorSession{complete: true, messages: mentorMinMessages}

	headers := serverMetadataHeaders(t, "compaction", nil)
	headers.Set(threadIDHeader, "main")
	compaction := serverRequest(t, func(fields map[string]any) {
		fields["model"] = "gpt-6-astra"
		fields["stream"] = true
	})
	if err := executeRequest(t.Context(), t.Context(), compaction, headers, "main",
		provider, io.Discard, nil, nil, mentor); err != nil {
		t.Fatal(err)
	}

	headers = serverMetadataHeaders(t, "turn", nil)
	headers.Set(threadIDHeader, "main")
	if err := executeRequest(t.Context(), t.Context(), mentorTestRequest(t, "gpt-5.6-luna"), headers, "main",
		provider, io.Discard, nil, nil, mentor); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(provider.forwarded[1])
	if err != nil || request.modelDescription() != "gpt-5.6-luna medium" {
		t.Fatalf("post-steering request = %q, err=%v, want configured model", request.modelDescription(), err)
	}
	if state := mentor.sessions["main"]; !state.complete || state.messages != mentorMinMessages {
		t.Fatalf("steered compaction changed schedule: %+v", state)
	}
}

func TestExecuteRequestMainAstraMentorHandoff(t *testing.T) {
	for _, model := range []string{"gpt-5.6", "gpt-5.6-sol"} {
		t.Run(model, func(t *testing.T) {
			testExecuteRequestMainAstraMentorHandoff(t, model)
		})
	}
}

func testExecuteRequestMainAstraMentorHandoff(t *testing.T, model string) {
	provider := &serverFakeProvider{}
	for range 3 {
		provider.results = append(provider.results, serverForwardResult{
			response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
				"status": "completed",
				"output": mentorTestItems(t, "message"),
				"usage":  map[string]any{"input_tokens": 1_000},
			}))),
		})
	}
	headers := serverMetadataHeaders(t, "turn", nil)
	headers.Set(threadIDHeader, "main")
	mentor := newMentorHandoff(true, true)
	for range 3 {
		request := mentorTestRequest(t, model)
		if err := request.setModelAndReasoningEffort(model, "high"); err != nil {
			t.Fatal(err)
		}
		if err := executeRequest(t.Context(), t.Context(), request, headers, "main",
			provider, io.Discard, nil, nil, mentor); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range []string{"gpt-6-astra medium", "gpt-6-astra medium", model + " high"} {
		request, err := parseResponsesRequest(provider.forwarded[index])
		if err != nil {
			t.Fatal(err)
		}
		if request.modelDescription() != want || !bytes.Contains(request.fields["input"], []byte("keep exact history")) {
			t.Fatalf("request %d = %q, input=%s", index, request.modelDescription(), request.fields["input"])
		}
	}
	// Completing main must not complete a newly spawned child's schedule.
	childHeaders := mentorTestHeaders(t, "child")
	metadata, valid := decodeCodexTurnMetadata(childHeaders)
	child := mentorTestRequest(t, "gpt-5.6")
	if handoff, err := mentor.prepare(childHeaders, metadata, valid, &child); err != nil || handoff == nil {
		t.Fatalf("child handoff=%v err=%v", handoff, err)
	}
}

func TestExecuteRequestMentorHandoffCountsFailedResponseInput(t *testing.T) {
	failed := serverHTTPResponse(string(mustTestJSON(t, map[string]any{
		"status": "failed",
		"usage":  map[string]any{"input_tokens": 55_000},
	})))
	completed := serverHTTPResponse(string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage":  map[string]any{"input_tokens": 1},
	})))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: failed}, {response: completed}}}
	mentor := newMentorHandoff(true, true)
	headers := mentorTestHeaders(t, "child")
	for range 2 {
		request := mentorTestRequest(t, "gpt-5.6-luna")
		if err := executeRequest(
			t.Context(), t.Context(), request, headers, "session", provider, io.Discard,
			nil, nil, mentor,
		); err != nil {
			t.Fatal(err)
		}
	}
	first, err := parseResponsesRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseResponsesRequest(provider.forwarded[1])
	if err != nil {
		t.Fatal(err)
	}
	if first.model() != mentorLeaderModel || second.model() != "gpt-5.6-luna" {
		t.Fatalf("models after failed-response budget = %q, %q", first.model(), second.model())
	}
}

func TestMentorHandoffRejectsInvalidReasoningWithoutSharingSessionCapacity(t *testing.T) {
	mentor := newMentorHandoff(true, true)
	headers := mentorTestHeaders(t, "child")
	metadata, valid := decodeCodexTurnMetadata(headers)
	request := mentorTestRequest(t, "gpt-5.6-luna")
	request.fields["reasoning"] = json.RawMessage(`"invalid"`)
	if _, err := mentor.prepare(headers, metadata, valid, &request); err == nil {
		t.Fatal("invalid reasoning was accepted")
	}

	mentor = newMentorHandoff(true, true)
	for index := range maxSessionHistories {
		mentor.sessions[fmt.Sprintf("active-%d", index)] = mentorSession{}
	}
	request = mentorTestRequest(t, "gpt-5.6-luna")
	prepared, err := mentor.prepare(headers, metadata, valid, &request)
	if err != nil || prepared == nil {
		t.Fatalf("new child at session history limit = %#v, %v", prepared, err)
	}
}

func TestMentorHandoffLunaMainMappingKeepsSubagentsUnchanged(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra", ""} {
		for _, child := range []bool{false, true} {
			headers := mentorTestHeaders(t, "thread")
			if !child {
				headers.Del(openAISubagentHeader)
				headers.Set(codexTurnMetadataHeader, `{"request_kind":"turn"}`)
			}
			request := mentorTestRequest(t, "gpt-5.6-luna")
			if effort == "" {
				delete(request.fields, "reasoning")
			} else if err := request.setModelAndReasoningEffort("gpt-5.6-luna", effort); err != nil {
				t.Fatal(err)
			}
			metadata, valid := decodeCodexTurnMetadata(headers)
			handoff, err := newMentorHandoff(true, true).prepare(headers, metadata, valid, &request)
			want := "gpt-6-astra medium"
			if child {
				want = "gpt-6-sol high"
			}
			if err != nil || handoff == nil || request.modelDescription() != want {
				t.Fatalf("child=%t effort=%q: model=%q handoff=%v err=%v", child, effort, request.modelDescription(), handoff, err)
			}
		}
	}
}

func TestExecuteRequestMentorCommentaryDeliveredOnceAndStrippedOnReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			mentor := newMentorHandoff(true, true)
			workspace := t.TempDir()
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
			headers.Set(threadIDHeader, "main")
			var replay []any
			for index := range 4 {
				outputItems := []any{}
				if index == 1 {
					outputItems = append(outputItems, map[string]any{
						"id": "switched-answer", "type": "message", "role": "assistant", "phase": "final_answer",
						"status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer after Mentor switched back."}},
					})
				}
				responseBody := mustTestJSON(t, map[string]any{
					"id":     fmt.Sprintf("mentor-response-%d", index),
					"status": "completed",
					"output": outputItems,
					"usage":  map[string]any{"input_tokens": mentorInputTokenLimit},
				})
				response := serverHTTPResponse(string(responseBody))
				if stream {
					events := [][]byte{}
					if index == 1 {
						events = append(events, mustTestJSON(t, map[string]any{
							"type": "response.output_item.done", "output_index": 0, "item": outputItems[0],
						}))
					}
					events = append(events, mustTestJSON(t, map[string]any{
						"type": "response.completed", "response": json.RawMessage(responseBody),
					}))
					response = serverHTTPResponse(finalAnswerTestWire(events))
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				request := serverRequest(t, func(request map[string]any) {
					request["model"] = "gpt-5.6-luna"
					request["stream"] = stream
					request["tools"] = testNativeResponsesTools()
					request["input"] = replay
				})
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "session",
					provider, &output, nil, proxy, mentor); err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(output.Bytes(), []byte("Mentor handoff complete.")) {
					t.Fatalf("response %d emitted a standalone handoff notice: %s", index, output.Bytes())
				}
				forwarded, err := parseResponsesRequest(provider.forwarded[0])
				if err != nil {
					t.Fatal(err)
				}
				if index == 0 {
					if forwarded.model() != "gpt-6-astra" || bytes.Contains(output.Bytes(), []byte("Router session usage")) {
						t.Fatalf("pre-switch completion made an early usage claim: model=%q output=%s", forwarded.model(), output.Bytes())
					}
				}
				if index == 1 {
					if forwarded.model() != "gpt-5.6-luna" || !bytes.Contains(output.Bytes(), []byte("Router session usage · Main turn:")) ||
						!bytes.Contains(output.Bytes(), []byte("Mentor gpt-6-astra → gpt-5.6-luna")) {
						t.Fatalf("first post-switch report lacks the actual transition: model=%q output=%s", forwarded.model(), output.Bytes())
					}
					for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
						text := commentaryMessageText(item)
						if strings.Contains(text, "Router session usage") || strings.Contains(text, "Journal flush") {
							replay = append(replay, assistantCommentaryMessage(jsonString(item, "id"), text))
						}
					}
					if len(replay) != 2 {
						t.Fatalf("generated-message provenance = %d messages, want usage and journal flush", len(replay))
					}
				}
				if index == 2 && (bytes.Contains(provider.forwarded[0], []byte("Router session usage")) || bytes.Contains(provider.forwarded[0], []byte("Journal flush"))) {
					t.Fatal("generated usage report or journal flush leaked into provider history")
				}
			}
		})
	}
}

func TestExecuteRequestMentorJournalContinuationAccounting(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, bound := range []string{"tokens", "messages", "tools"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, bound), func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				mentor := newMentorHandoff(true, true)
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				threadID := codexThreadID(headers)
				provider := &serverFakeProvider{}
				count := 2
				if bound == "tools" {
					count = 5
				}
				for index := range count {
					call := map[string]any{"type": "function_call", "id": fmt.Sprintf("item-%d", index), "call_id": fmt.Sprintf("call-%d", index), "name": "journal", "arguments": `{"op":"list"}`, "status": "completed"}
					if index == count-1 {
						call["arguments"] = `{"op":"finish"}`
					}
					items := []any{call}
					if bound == "messages" && index == 0 {
						for message := range 2 {
							items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("message-%d", message), "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "Progress"}}})
						}
					}
					tokens := uint64(100 + index)
					if bound == "tokens" && index == 0 {
						tokens = mentorInputTokenLimit
					}
					body := mustTestJSON(t, map[string]any{"id": fmt.Sprintf("response-%d", index), "status": "completed", "output": items, "usage": map[string]any{"input_tokens": tokens}})
					response := serverHTTPResponse(string(body))
					if stream {
						var events [][]byte
						for _, item := range items {
							events = append(events, mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item}))
						}
						events = append(events, mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(body)}))
						response = serverHTTPResponse(finalAnswerTestWire(events))
						response.Header.Set("Content-Type", "text/event-stream")
					}
					provider.results = append(provider.results, serverForwardResult{response: response})
				}
				request := serverRequest(t, func(fields map[string]any) {
					fields["model"] = "gpt-5.6-luna"
					fields["stream"] = stream
				})
				if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, io.Discard, nil, proxy, mentor); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != count {
					t.Fatalf("requests = %d, want %d", len(provider.forwarded), count)
				}
				for index, body := range provider.forwarded {
					request, err := parseResponsesRequest(body)
					if err != nil {
						t.Fatal(err)
					}
					want := "gpt-6-astra"
					if index == count-1 {
						want = "gpt-5.6-luna"
					}
					if request.model() != want {
						t.Errorf("request %d model = %q, want %q", index, request.model(), want)
					}
				}
				state := mentor.sessions[threadID]
				wantTools, wantMessages, wantUsage := uint64(1), uint64(0), mentorInputTokenLimit
				if bound == "messages" {
					wantMessages, wantUsage = 2, 100
				}
				if bound == "tools" {
					wantTools, wantUsage = 4, 103
				}
				if !state.complete || state.awaitingToolResult || state.toolCalls != wantTools || state.messages != wantMessages || state.latestInputTokens != wantUsage {
					t.Fatalf("final schedule = %+v; want complete, tools=%d messages=%d latest usage=%d", state, wantTools, wantMessages, wantUsage)
				}
			})
		}
	}
}

func TestExecuteRequestChildNonTurnsHandleMentorSchedule(t *testing.T) {
	for _, kind := range []string{"prewarm", "compaction", ""} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", kind, existing), func(t *testing.T) {
				mentor := newMentorHandoff(true, true)
				before := mentorSession{latestInputTokens: 123, toolCalls: 3, awaitingToolResult: true}
				if existing {
					mentor.sessions["child"] = before
				}
				headers := mentorTestHeaders(t, "child")
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, codexTurnMetadata{RequestKind: responses.RequestKind(kind), SubagentKind: threadSpawnSubagentKind})))
				provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
					"status": "completed", "output": mentorTestItems(t, "message", "message"),
					"usage": map[string]any{"input_tokens": mentorInputTokenLimit},
				})))}}}
				request := mentorTestRequest(t, "gpt-5.6-luna")
				if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, io.Discard, nil, nil, mentor); err != nil {
					t.Fatal(err)
				}
				forwarded, err := parseResponsesRequest(provider.forwarded[0])
				if err != nil || forwarded.modelDescription() != "gpt-5.6-luna medium" {
					t.Fatalf("non-turn model = %q, err=%v", forwarded.modelDescription(), err)
				}
				state, exists := mentor.sessions["child"]
				if kind == "compaction" {
					if exists {
						t.Fatalf("successful compaction did not reset schedule: %+v", state)
					}
				} else if exists != existing || (existing && state != before) {
					t.Fatalf("non-turn changed schedule: %+v, exists=%t", state, exists)
				}
			})
		}
	}
}
