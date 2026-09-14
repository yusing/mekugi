package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	responseevents "github.com/yusing/mekugi/internal/responses"

	"github.com/coder/websocket"
)

func queueCritical(c *CriticalErrors, session string) {
	c.record(&requestFinalization{sessionID: session, failurePhase: requestFailurePrepare,
		observation: requestObservation{outcome: requestOutcomeFailed}}, incompatibleRequest("unsupported_tool_catalog", "Enable supported tools."))
}

func TestCriticalErrorsDeduplicateReserveAndRetainUntilDelivery(t *testing.T) {
	c := NewCriticalErrors()
	queueCritical(c, "one")
	queueCritical(c, "one")
	if pending := c.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "2 times") {
		t.Fatalf("pending = %v", pending)
	}
	first := c.transform("one", false)
	if len(first.messages) != 1 || len(c.transform("two", false).messages) != 0 || len(c.transform("one", false).messages) != 0 {
		t.Fatal("notice crossed a session or concurrent response")
	}
	first.finish(false)
	retry := c.transform("one", false)
	if len(retry.messages) != 1 {
		t.Fatal("failed delivery lost notice")
	}
	body, err := retry.TransformJSON([]byte(`{"status":"completed","output":[{"type":"message","id":"answer","content":[]}]}`))
	if err != nil || !strings.Contains(string(body), "Enable supported tools.") {
		t.Fatalf("JSON = %s, %v", body, err)
	}
	retry.finish(true)
	if len(c.Pending()) != 0 {
		t.Fatal("delivered notice still pending")
	}
	queueCritical(c, "one")
	if len(c.transform("one", false).messages) != 0 {
		t.Fatal("repeat flooded the session")
	}
	if pending := c.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "3 times") {
		t.Fatalf("repeat summary = %v", pending)
	}
}

func TestCriticalErrorsKeepDistinctSafeCausesAndHideExternalPayloads(t *testing.T) {
	c := NewCriticalErrors()
	record := func(err error) {
		c.record(&requestFinalization{sessionID: "one", failurePhase: requestFailureTransform,
			observation: requestObservation{outcome: requestOutcomeFailed}}, err)
	}
	first := staticCriticalDiagnostic("stream_ended_incomplete_mekugi_call", "the upstream stream ended with an incomplete HPATCH call")
	record(first)
	record(first)
	record(staticCriticalDiagnostic("malformed_mekugi_call", "the upstream emitted a malformed HPATCH call"))
	if len(c.entries) != 2 || c.entries[0].count != 2 || c.entries[1].count != 1 {
		t.Fatalf("safe causes collapsed or did not deduplicate: %+v", c.entries)
	}
	pending := strings.Join(c.Pending(), "\n")
	for _, want := range []string{"incomplete HPATCH call", "malformed HPATCH call", "occurred 2 times", "Diagnostic reference:"} {
		if !strings.Contains(pending, want) {
			t.Fatalf("pending notice lacks %q: %s", want, pending)
		}
	}

	external := NewCriticalErrors()
	secret := "Authorization: Bearer token-plain prompt unquoted-secret-script"
	external.record(&requestFinalization{sessionID: "one", failurePhase: requestFailureTransform,
		observation: requestObservation{outcome: requestOutcomeFailed}}, errors.New(secret))
	externalNotice := strings.Join(external.Pending(), "\n")
	if strings.Contains(externalNotice, "token-plain") || strings.Contains(externalNotice, "unquoted-secret-script") ||
		!strings.Contains(externalNotice, "not safe for display") || !strings.Contains(externalNotice, "Diagnostic reference:") {
		t.Fatalf("external payload was exposed or safe fallback was absent: %s", externalNotice)
	}

	unsafeEvent := NewCriticalErrors()
	unsafeEventName := "unquotedsecretpayload"
	unsafeEvent.record(&requestFinalization{sessionID: "one", failurePhase: requestFailureTransform,
		observation: requestObservation{outcome: requestOutcomeFailed}}, unsupportedMekugiStreamEvent(responseevents.Kind(unsafeEventName)))
	unsafeEventNotice := strings.Join(unsafeEvent.Pending(), "\n")
	if strings.Contains(unsafeEventNotice, unsafeEventName) || !strings.Contains(unsafeEventNotice, "unsupported HPATCH-related streaming event") {
		t.Fatalf("untrusted protocol value was exposed or hid its safe cause: %s", unsafeEventNotice)
	}
}

func TestCriticalErrorsIdentifyEveryGenericFailurePhaseWithoutExposingCauses(t *testing.T) {
	for _, test := range []struct {
		phase requestFailurePhase
		label string
	}{
		{requestFailureForward, "upstream request forwarding"},
		{requestFailureInspectResponse, "upstream response inspection"},
		{requestFailureWriteResponse, "downstream response writing"},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			c := NewCriticalErrors()
			record := func(err error) {
				c.record(&requestFinalization{sessionID: "one", failurePhase: test.phase,
					observation: requestObservation{outcome: requestOutcomeFailed}}, err)
			}
			first := errors.New("Authorization Bearer firstsecret")
			record(first)
			record(first)
			record(errors.New("plain secondsecret prompt"))
			if len(c.entries) != 2 || c.entries[0].count != 2 || c.entries[1].count != 1 {
				t.Fatalf("non-transform causes collapsed or did not deduplicate: %+v", c.entries)
			}
			pending := strings.Join(c.Pending(), "\n")
			if strings.Contains(pending, "firstsecret") || strings.Contains(pending, "secondsecret") ||
				strings.Count(pending, "Failure phase: "+test.label+".") != 2 ||
				strings.Count(pending, "Diagnostic reference:") != 2 {
				t.Fatalf("generic notices were unsafe or ambiguous: %s", pending)
			}
		})
	}

	terminal := NewCriticalErrors()
	terminal.record(&requestFinalization{sessionID: "one", failurePhase: requestFailureTerminalValidation,
		observation: requestObservation{outcome: requestOutcomeFailed}}, fmt.Errorf("validate response: %w", errUpstreamResponseWithoutTerminal))
	terminalNotice := strings.Join(terminal.Pending(), "\n")
	if !strings.Contains(terminalNotice, "Failure phase: terminal response validation.") ||
		!strings.Contains(terminalNotice, "ended without a completed or failed terminal state") {
		t.Fatalf("known terminal cause was hidden: %s", terminalNotice)
	}

	prepare := NewCriticalErrors()
	prepare.record(&requestFinalization{sessionID: "one", failurePhase: requestFailurePrepare,
		observation: requestObservation{outcome: requestOutcomeFailed}}, errors.New("plain preparesecret"))
	prepareNotice := strings.Join(prepare.Pending(), "\n")
	if strings.Contains(prepareNotice, "check the request error") || strings.Contains(prepareNotice, "preparesecret") ||
		!strings.Contains(prepareNotice, "Failure phase: request preparation.") {
		t.Fatalf("prepare notice was not self-contained: %s", prepareNotice)
	}
}

func TestCriticalErrorsReplayRemovesOnlyOwnedSessionMessages(t *testing.T) {
	c := NewCriticalErrors()
	queueCritical(c, "one")
	queueCritical(c, "two")
	first := c.transform("one", false)
	other := c.transform("two", false)
	authored := assistantCommentaryMessage("model-owned", "Enable supported tools.")
	request := serverRequest(t, func(fields map[string]any) { fields["input"] = []any{first.messages[0], other.messages[0], authored} })
	c.stripInput(&request, "one")
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 2 || jsonString(input[0], "id") != jsonString(other.messages[0], "id") || jsonString(input[1], "id") != "model-owned" {
		t.Fatalf("input = %s", request.fields["input"])
	}
}

func TestCriticalErrorsStreamingPreservesSubagentResult(t *testing.T) {
	for _, subagent := range []bool{false, true} {
		c := NewCriticalErrors()
		queueCritical(c, "one")
		tr := c.transform("one", subagent)
		events, err := tr.TransformSSE([]byte(`{"type":"response.created","response":{"id":"response"}}`))
		if err != nil || len(events) != map[bool]int{false: 2, true: 1}[subagent] {
			t.Fatalf("created: %d, %v", len(events), err)
		}
		events, err = tr.TransformSSE([]byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"answer","content":[]}]}}`))
		if err != nil || len(events) != 1 {
			t.Fatalf("terminal: %d, %v", len(events), err)
		}
		var terminal struct {
			Response struct{ Output []map[string]json.RawMessage }
		}
		if err := json.Unmarshal(events[0], &terminal); err != nil {
			t.Fatal(err)
		}
		if len(terminal.Response.Output) != 2 || jsonString(terminal.Response.Output[1], "id") != "answer" {
			t.Fatalf("substantive result replaced: %s", events[0])
		}
		tr.finish(true)
		if len(c.Pending()) != 0 {
			t.Fatal("stream delivery not acknowledged")
		}
	}
}

func TestCriticalErrorsBoundedAndCancellationSilent(t *testing.T) {
	c := NewCriticalErrors()
	for _, outcome := range []requestOutcome{requestOutcomeCompleted, requestOutcomeCanceledBeforeResponse, requestOutcomeCanceledAfterResponse} {
		c.record(&requestFinalization{observation: requestObservation{outcome: outcome}}, context.Canceled)
	}
	if len(c.Pending()) != 0 {
		t.Fatal("success or cancellation emitted a notice")
	}
	for i := range 300 {
		queueCritical(c, fmt.Sprint(i))
	}
	if len(c.entries) != 256 || c.overflow != 44 || len(c.Pending()) != 257 {
		t.Fatal("unbounded error queue or missing overflow summary")
	}
}

func TestPermanentRewriteFailureIsBadRequestAndQueued(t *testing.T) {
	c := NewCriticalErrors()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	provider := &serverFakeProvider{}
	request := serverRequest(t, func(fields map[string]any) { fields["tool_choice"] = map[string]any{"type": "custom", "name": "exec"} })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(request.originalBody)))
	req.Header = serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
	req.Header.Set(sessionIDHeader, "one")
	output := httptest.NewRecorder()
	responsesHandler(t.Context(), time.Minute, provider, c, proxy, nil, nil)(output, req)
	if output.Code != 400 || !strings.Contains(output.Body.String(), "restricted_tool_choice") || len(provider.forwarded) != 0 {
		t.Fatalf("response %d: %s", output.Code, output.Body.String())
	}
	if len(c.Pending()) != 1 {
		t.Fatal("permanent failure was not retained")
	}
}

func TestRequestCompatibilityMissingNativeTools(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []any
		code  string
	}{
		{"editing", []any{map[string]any{"type": "function", "name": "exec_command"}}, "missing_apply_patch"},
		{"execution", []any{map[string]any{"type": "custom", "name": "apply_patch"}}, "missing_exec_command"},
	} {
		fields := map[string]json.RawMessage{"tools": mustTestJSON(t, test.tools)}
		_, replaced, err := replaceNativeTools(fields, decodeResponsesToolCatalog(fields), testInstalledTools())
		compatibility, ok := errors.AsType[*requestCompatibilityError](err)
		if replaced || !ok || compatibility.code != test.code {
			t.Fatalf("%s: %v", test.name, err)
		}
	}
}

func TestInvalidNativeCatalogIsBadRequestBeforeForwarding(t *testing.T) {
	tool := func(kind, name string) any { return map[string]any{"type": kind, "name": name} }
	for _, tools := range [][]any{
		{tool("function", "apply_patch"), tool("function", "exec_command")},
		{tool("custom", "apply_patch"), tool("custom", "exec_command")},
		{tool("custom", "apply_patch"), tool("custom", "apply_patch"), tool("function", "exec_command")},
		{tool("custom", "apply_patch"), tool("function", "exec_command"), tool("function", "exec_command")},
		{tool("custom", "apply_patch"), tool("function", "exec_command"), tool("custom", "hpatch")},
	} {
		proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
		provider := &serverFakeProvider{}
		parsed := serverRequest(t, func(fields map[string]any) { fields["input"] = []any{}; fields["tools"] = tools })
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(parsed.originalBody)))
		request.Header = serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
		request.Header.Set(sessionIDHeader, "one")
		output := httptest.NewRecorder()
		responsesHandler(t.Context(), time.Minute, provider, NewCriticalErrors(), proxy, nil, nil)(output, request)
		if output.Code != 400 || len(provider.forwarded) != 0 {
			t.Fatalf("catalog reached upstream or remained retryable: %d %s", output.Code, output.Body.String())
		}
	}
}

func TestForwardFailureDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{"handshake401", &webSocketStatusError{status: 401, body: []byte("private")}, "upstream_websocket_http_401"},
		{"handshake403", &webSocketStatusError{status: 403, body: []byte("private")}, "upstream_websocket_http_403"},
		{"handshake429", &webSocketStatusError{status: 429, body: []byte("private")}, "upstream_websocket_http_429"},
		{"eof", fmt.Errorf("private URL: %w", io.EOF), "upstream_eof"},
		{"unexpected eof", io.ErrUnexpectedEOF, "upstream_unexpected_eof"},
		{"reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, "upstream_connection_reset"},
		{"refused", syscall.ECONNREFUSED, "upstream_connection_refused"},
		{"broken pipe", syscall.EPIPE, "upstream_broken_pipe"},
		{"dns", &net.DNSError{Err: "private DNS detail", Name: "private host"}, "upstream_dns"},
		{"write", &net.OpError{Op: "write", Err: errors.New("private detail")}, "upstream_network_write"},
		{"close", websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "private close reason"}, "upstream_websocket_close_1008"},
		{"deadline", context.DeadlineExceeded, "upstream_deadline"},
		{"canceled", context.Canceled, "upstream_canceled"},
		{"unknown", errors.New("private unknown cause"), "upstream_transport_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			flags := newRouterFlags(io.Discard)
			*flags.debug = true
			debug, err := openDebugOutput(flags)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = debug.close() })
			ctx := context.WithValue(t.Context(), debugContextKey{}, debug)
			request, err := parseResponsesRequest(mustTestJSON(t, titleRequestFields()))
			if err != nil {
				t.Fatal(err)
			}
			issues := NewCriticalErrors()
			provider := &serverFakeProvider{results: []serverForwardResult{{err: test.err}}}
			err = executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, io.Discard, issues, nil, nil, nil)
			if !errors.Is(err, test.err) {
				t.Fatalf("original cause lost: %v", err)
			}
			data, err := os.ReadFile(debug.paths[0])
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var record map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
				t.Fatal(err)
			}
			if record["diagnostic_code"] != test.code || record["phase"] != "forward" {
				t.Fatalf("wrong diagnostic: %v", record)
			}
			reference, _ := record["diagnostic_reference"].(string)
			if len(reference) != 12 {
				t.Fatalf("missing reference: %v", record)
			}
			notices := strings.Join(issues.Pending(), "\n")
			if rejection, ok := errors.AsType[*webSocketStatusError](test.err); ok {
				if record["upstream_status"] != float64(rejection.status) {
					t.Fatalf("handshake status lost: %v", record)
				}
				guidance := "credentials"
				if rejection.status == 429 {
					guidance = "Wait before retrying"
				}
				if !strings.Contains(notices, guidance) {
					t.Fatalf("missing handshake guidance: %s", notices)
				}
			}
			if !strings.Contains(notices, reference) {
				t.Fatalf("notice/reference mismatch: %s", notices)
			}
			if strings.Contains(string(data)+notices, "private") {
				t.Fatal("diagnostic leaked external error text")
			}
		})
	}
}

func TestCriticalSynthesizedDiagnosticCodes(t *testing.T) {
	for _, test := range []struct {
		body   string
		output io.Writer
		code   string
	}{
		{`{"status":"in_progress","output":[]}`, io.Discard, "missing_upstream_terminal:pending"},
		{`{"status":"completed","output":[]}`, serverErrorWriter{err: errors.New("private write failure")}, "downstream_response_write"},
		{`{"status":"failed","output":[]}`, io.Discard, "upstream_failed"},
	} {
		t.Run(test.code, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			flags := newRouterFlags(io.Discard)
			*flags.debug = true
			debug, err := openDebugOutput(flags)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = debug.close() })
			ctx := context.WithValue(t.Context(), debugContextKey{}, debug)
			request, err := parseResponsesRequest(mustTestJSON(t, titleRequestFields()))
			if err != nil {
				t.Fatal(err)
			}
			issues := NewCriticalErrors()
			provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(test.body)),
			}}}}
			_ = executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, test.output, issues, nil, nil, nil)
			data, err := os.ReadFile(debug.paths[0])
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var record map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
				t.Fatal(err)
			}
			notices := strings.Join(issues.Pending(), "\n")
			reference, _ := record["diagnostic_reference"].(string)
			if record["diagnostic_code"] != test.code || len(reference) != 12 || !strings.Contains(notices, reference) ||
				len(issues.entries) != 1 || !strings.Contains(issues.entries[0].category, test.code) {
				t.Fatalf("diagnostics disagree: %v, %s", record, notices)
			}
			if strings.Contains(string(data)+notices, "private") {
				t.Fatal("diagnostic leaked external error text")
			}
		})
	}
}
