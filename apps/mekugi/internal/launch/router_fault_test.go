package launch

import (
	"io"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"

	json "encoding/json/v2"
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
	issues := testCriticalErrors()
	metadata := codexresponses.TurnMetadata{
		RequestKind: "turn", ThreadID: "router-fault-thread", TurnID: "router-fault-turn",
		Directories: map[string]jsonv1.RawMessage{workspace: nil},
	}
	headers := serverMetadataHeaders(t, "turn", metadata.Directories)
	headers.Set(codexresponses.ThreadIDHeader, metadata.ThreadID)
	headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
	recorder := httptest.NewRecorder()
	err = executeRequest(t.Context(), t.Context(), request, headers, "router-fault-session",
		provider, &inferencegateway.ResponseWriter{ResponseWriter: recorder}, issues, proxy)
	if err == nil {
		t.Fatal("malformed intercepted event unexpectedly completed")
	}

	events := routerFaultEvents(t, recorder.Body.String())
	if len(events) < 2 || routerFaultEventString(events[0], "type") != "response.created" || routerFaultEventString(events[len(events)-1], "type") != "response.failed" {
		t.Fatalf("stream did not end with a terminal failure after response.created: err=%v forwarded=%d status=%d issues=%+v body=%q", err, len(provider.forwarded), recorder.Code, issues.Notices.Pending(), recorder.Body.String())
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
	if len(issues.Notices.Pending()) != 1 || strings.Contains(issues.Notices.Pending()[0], "occurred") || !strings.Contains(issues.Notices.Pending()[0], message) {
		t.Fatalf("fault did not produce exactly one actionable notice: %+v", issues.Notices.Pending())
	}
	if pending := issues.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], message) {
		t.Fatalf("transport write acknowledged an unpainted notice: %v", pending)
	}
	ui, _ := newAppServerTestUI()
	ui.Thread, ui.issues = metadata.ThreadID, issues
	if applyCriticalNotices(&ui.attached, ui.shell, &ui.Client, &ui.picker, &ui.Editor, ui.clock, preferences.Applied{Model: ui.model, Effort: ui.reasoningEffort, Tier: ui.serviceTier}, ui.serviceTiers, ui.title, ui.backendVersion, &ui.restoring, &ui.side, ui.audience, ui.issues) != nil {
		t.Fatal("request notice interrupted a retrying turn")
	}
	appServerTestNotify(t, ui, "turn/completed", map[string]any{"threadId": metadata.ThreadID, "turn": map[string]any{"id": metadata.TurnID, "status": "failed"}})
	delivery := applyCriticalNotices(&ui.attached, ui.shell, &ui.Client, &ui.picker, &ui.Editor, ui.clock, preferences.Applied{Model: ui.model, Effort: ui.reasoningEffort, Tier: ui.serviceTier}, ui.serviceTiers, ui.title, ui.backendVersion, &ui.restoring, &ui.side, ui.audience, ui.issues)
	if delivery == nil || !strings.Contains(ui.attached.feedback.Text, message) {
		t.Fatal("host-confirmed failure lost native notice delivery")
	}

	defer terminalTest(ui).Diff.Close()
	if err := ui.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	delivery.finish(terminalTest(ui).Frame.Notices)
	if pending := issues.Notices.Pending(); len(pending) != 0 {
		t.Fatalf("visible terminal paint did not acknowledge notice: %v", pending)
	}
}

func TestRouterFaultNoticeDeduplicatesWithinTurnButSeparatesTurns(t *testing.T) {
	issues := testCriticalErrors()
	makeFault := func(turn string) requestexecution.Failure {
		return requestexecution.Failure{
			Session: "fault-session", Thread: "fault-thread", Turn: turn,
			Phase: "transform",
		}
	}
	fault := &requestexecution.DiagnosticError{Code: "mekugi_sse:inconsistent_stock_call", Summary: "the upstream completed an inconsistent stock tool call", Distinct: true, Cause: requestexecution.StaticError("inconsistent_stock_call", "the upstream completed an inconsistent stock tool call")}

	first := makeFault("turn-one")
	firstResult := issues.Report(first, fault)
	retry := makeFault("turn-one")
	retryResult := issues.Report(retry, fault)
	if firstResult.Reference == "" || retryResult.Reference != firstResult.Reference || len(issues.Notices.Pending()) != 1 || strings.Contains(issues.Notices.Pending()[0], "occurred") {
		t.Fatalf("same-turn retry was not deduplicated: first=%+v retry=%+v entries=%+v", first, retry, issues.Notices.Pending())
	}

	nextTurn := makeFault("turn-two")
	nextTurnResult := issues.Report(nextTurn, fault)
	if nextTurnResult.Reference != firstResult.Reference || len(issues.Notices.Pending()) != 2 ||
		strings.Contains(issues.Notices.Pending()[0], "occurred") || strings.Contains(issues.Notices.Pending()[1], "occurred") ||
		firstResult.Reference == "" {
		t.Fatalf("same fault on a new turn did not receive a distinct notice: %+v", issues.Notices.Pending())
	}
	issues.Notices.ObserveTurn("fault-thread", "turn-one", "failed")
	issues.Notices.ObserveTurn("fault-thread", "turn-two", "failed")
	claim := claimNativeNotices(issues, "fault-thread", nil)
	if claim == nil || len(claim.Errors.Messages) != 2 || claim.Errors.Messages[0].ID == claim.Errors.Messages[1].ID {
		t.Fatalf("distinct turns lost notice identities: %+v", claim)
	}
	claim.Errors.Finish(false)
}
