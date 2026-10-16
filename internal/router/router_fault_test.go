package router

import (
	"bytes"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"

	json "encoding/json/v2"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func routerFaultSSE(events ...any) string {
	var stream strings.Builder
	for _, event := range events {
		stream.WriteString("data: ")
		stream.Write(responses.MustMarshalJSON(event))
		stream.WriteString("\n\n")
	}
	return stream.String()
}

func routerFaultEventString(event map[string]jsontext.Value, field string) string {
	var value string
	_ = json.Unmarshal(event[field], &value)
	return value
}

func routerFaultEvents(t *testing.T, wire string) []map[string]jsontext.Value {
	t.Helper()
	var events []map[string]jsontext.Value
	for frame := range strings.SplitSeq(wire, "\n\n") {
		for line := range strings.SplitSeq(frame, "\n") {
			data, found := strings.CutPrefix(line, "data: ")
			if !found || data == "[DONE]" {
				continue
			}
			var event map[string]jsontext.Value
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("decode SSE event %q: %v", data, err)
			}
			events = append(events, event)
		}
	}
	return events
}

func TestRouterTransformFaultAfterCreatedEmitsActionableTerminalFailure(t *testing.T) {
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)

	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "stream": true,
		"input": []any{map[string]any{"role": "user", "content": "run a command"}},
		"tools": testExecResponsesTools(), "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]any{"type": "response.created", "response": map[string]any{"id": "router-fault-response", "status": "in_progress", "output": []any{}}}
	added := map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
		"type": "custom_tool_call", "id": "router-fault-item", "call_id": "router-fault-call",
		"name": "exec", "input": "", "status": "in_progress",
	}}
	// The provider starts a stock command call and then changes the intercepted
	// item identity. The real response transform rejects the malformed done event.
	malformedDone := map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
		"type": "custom_tool_call", "id": "router-fault-item", "call_id": "router-fault-call",
		"name": "changed_exec", "input": "", "status": "completed",
	}}
	upstream := serverHTTPResponse(routerFaultSSE(created, added, malformedDone))
	upstream.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: upstream}}}
	issues := NewCriticalErrors()
	metadata := codexTurnMetadata{
		RequestKind: "turn", ThreadID: "router-fault-thread", TurnID: "router-fault-turn",
		Directories: map[string]jsonv1.RawMessage{workspace: nil},
	}
	headers := serverMetadataHeaders(t, "turn", metadata.Directories)
	headers.Set(codexresponses.ThreadIDHeader, metadata.ThreadID)
	headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
	recorder := httptest.NewRecorder()
	err = executeRequest(t.Context(), t.Context(), request, headers, "router-fault-session",
		provider, &trackedResponseWriter{ResponseWriter: recorder}, issues, proxy)
	if err == nil {
		t.Fatal("malformed intercepted event unexpectedly completed")
	}

	events := routerFaultEvents(t, recorder.Body.String())
	if len(events) < 2 || routerFaultEventString(events[0], "type") != "response.created" || routerFaultEventString(events[len(events)-1], "type") != "response.failed" {
		t.Fatalf("stream did not end with a terminal failure after response.created: err=%v forwarded=%d status=%d issues=%+v body=%q", err, len(provider.forwarded), recorder.Code, issues.entries, recorder.Body.String())
	}
	var failed struct {
		Response struct {
			ID    string `json:"id"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	lastEvent, err := json.Marshal(events[len(events)-1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lastEvent, &failed); err != nil {
		t.Fatal(err)
	}
	message := failed.Response.Error.Message
	for _, want := range []string{
		"Retrying will fail the same way",
		"Switch model",
		"passthrough mode",
		"--debug",
		"report the diagnostic reference",
		"mekugi_sse:inconsistent_stock_call",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("terminal failure message omitted %q: %s", want, message)
		}
	}
	if failed.Response.Error.Code != "invalid_prompt" || failed.Response.ID != "router-fault-response" {
		t.Fatalf("terminal failure = %+v, want invalid_prompt for the created response", failed.Response)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("upstream requests = %d, want one for a deterministic transform fault", len(provider.forwarded))
	}
	if len(issues.entries) != 1 || issues.entries[0].count != 1 || !strings.Contains(issues.entries[0].message, message) {
		t.Fatalf("fault did not produce exactly one actionable notice: %+v", issues.entries)
	}
}

func TestRouterFaultNoticeDeduplicatesWithinTurnButSeparatesTurns(t *testing.T) {
	issues := NewCriticalErrors()
	makeFault := func(turn string) requestFinalization {
		return requestFinalization{
			sessionID: "fault-session", threadID: "fault-thread", turnID: turn,
			failurePhase: requestFailureTransform,
			observation:  requestObservation{outcome: requestOutcomeFailed},
		}
	}
	fault := translationDiagnostic(staticCriticalDiagnostic(
		"inconsistent_stock_call", "the upstream completed an inconsistent stock tool call"),
		"mekugi_sse", "Mekugi response translation failed while processing an upstream streaming event")

	first := makeFault("turn-one")
	issues.record(&first, fault)
	retry := makeFault("turn-one")
	issues.record(&retry, fault)
	if first.diagnosticReference == "" || retry.diagnosticReference != first.diagnosticReference || len(issues.entries) != 1 || issues.entries[0].count != 1 {
		t.Fatalf("same-turn retry was not deduplicated: first=%+v retry=%+v entries=%+v", first, retry, issues.entries)
	}

	nextTurn := makeFault("turn-two")
	issues.record(&nextTurn, fault)
	if nextTurn.diagnosticReference != first.diagnosticReference || len(issues.entries) != 2 ||
		issues.entries[0].count != 1 || issues.entries[1].count != 1 ||
		issues.entries[0].id == issues.entries[1].id {
		t.Fatalf("same fault on a new turn did not receive a distinct notice: %+v", issues.entries)
	}
}

func TestRequestAttemptFinishDoesNotDuplicateDeliveredTerminal(t *testing.T) {
	output := new(bytes.Buffer)
	for _, event := range []string{
		`{"type":"response.created","response":{"id":"already-failed-response","status":"in_progress"}}`,
		`{"type":"response.failed","response":{"id":"already-failed-response","status":"failed","error":{"code":"invalid_prompt","message":"prior terminal failure"}}}`,
	} {
		if err := codexresponses.WriteEvent(output, []byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	attempt := &requestAttempt{
		executor:       requestExecutor{output: output, issues: NewCriticalErrors()},
		startCtx:       t.Context(),
		executionCtx:   t.Context(),
		hooks:          &responseHooks{deliveredResponseID: "already-failed-response", deliveredTerminal: true},
		streamResponse: true,
		finalization: requestFinalization{
			sessionID: "already-failed-session", threadID: "already-failed-thread", turnID: "already-failed-turn",
			failurePhase: requestFailureTransform,
		},
	}
	if err := attempt.finish(errors.New("a transform fault arrived after terminal delivery")); err == nil {
		t.Fatal("request finish lost the transform fault")
	}
	events := routerFaultEvents(t, output.String())
	failed := 0
	for _, event := range events {
		if routerFaultEventString(event, "type") == "response.failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("response.failed count = %d, want no duplicate terminal: %s", failed, output.String())
	}
}
