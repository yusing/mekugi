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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/modules/notifications/notice"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func queueCritical(c *requestexecution.Failures, session string) {
	c.Report(requestexecution.Failure{Session: session, Thread: session, Phase: "prepare"}, inferencegateway.IncompatibleRequest("unsupported_tool_catalog", "Enable supported tools."))
}

func TestCriticalErrorsDeduplicateReserveAndRetainUntilDelivery(t *testing.T) {
	c := testCriticalErrors()
	queueCritical(c, "one")
	queueCritical(c, "one")
	if pending := c.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "2 times") {
		t.Fatalf("pending = %v", pending)
	}
	first := claimNativeNotices(c, "one", nil)
	if first == nil || len(first.Errors.Messages) != 1 || claimNativeNotices(c, "two", nil) != nil || claimNativeNotices(c, "one", nil) != nil {
		t.Fatal("notice crossed a session or concurrent response")
	}
	first.Errors.Finish(false)
	retry := claimNativeNotices(c, "one", nil)
	if len(retry.Errors.Messages) != 1 || !strings.Contains(retry.Errors.Messages[0].Text, "Enable supported tools.") {
		t.Fatal("failed delivery lost notice")
	}
	retry.Errors.Finish(true)
	if len(c.Notices.Pending()) != 0 {
		t.Fatal("delivered notice still pending")
	}
	queueCritical(c, "one")
	if claimNativeNotices(c, "one", nil) != nil {
		t.Fatal("repeat flooded the session")
	}
	if pending := c.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "3 times") {
		t.Fatalf("repeat summary = %v", pending)
	}
}

func TestCriticalErrorsNativeOwnershipAndRouterWideNotice(t *testing.T) {
	c := testCriticalErrors()
	queueCritical(c, "one")
	queueCritical(c, "two")
	queueCritical(c, "child")
	c.AddNotice("", "router-capacity", "router-wide capacity warning")
	activity := newSubagentActivity()
	if !activity.observe("one", "", "/root", false) || !activity.observe("child", "one", "/root/child", true) {
		t.Fatal("failed to establish ancestry")
	}
	child := claimNativeNotices(c, "child", activity)
	if child != nil {
		t.Fatal("child stole native root notices")
	}
	root := claimNativeNotices(c, "one", activity)
	if root == nil || len(root.Errors.Messages) != 3 {
		t.Fatalf("root notices = %d, want own, child, and router-wide", len(root.Errors.Messages))
	}
	root.Errors.Finish(true)
	if len(c.Notices.Pending()) != 1 {
		t.Fatalf("unrelated session notice was acknowledged: %v", c.Notices.Pending())
	}
}

func TestInvalidNativeCatalogIsBadRequestBeforeForwarding(t *testing.T) {
	tool := func(kind, name string) any { return map[string]any{"type": kind, "name": name} }
	for _, tools := range [][]any{
		{tool("function", "apply_patch"), tool("function", "exec_command")},
		{tool("custom", "apply_patch"), tool("custom", "exec_command")},
		{tool("custom", "apply_patch"), tool("custom", "apply_patch"), tool("function", "exec_command")},
		{tool("custom", "apply_patch"), tool("function", "exec_command"), tool("function", "exec_command")},
	} {
		proxy := newManagedMekugiProxy(t)
		provider := &serverFakeProvider{}
		parsed := serverRequest(t, func(fields map[string]any) { fields["input"] = []any{}; fields["tools"] = tools })
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(requestTestBody(t, parsed))))
		request.Header = serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
		request.Header.Set(codexresponses.RoutingSessionHeader, "one")
		output := httptest.NewRecorder()
		testResponsesHandler(t.Context(), time.Minute, provider, testCriticalErrors(), proxy)(output, request)
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
		{"handshake401", fmt.Errorf("private: %w", &inferencegateway.WebSocketStatusError{Status: 401}), "upstream_websocket_http_401"},
		{"handshake403", fmt.Errorf("private: %w", &inferencegateway.WebSocketStatusError{Status: 403}), "upstream_websocket_http_403"},
		{"handshake429", fmt.Errorf("private: %w", &inferencegateway.WebSocketStatusError{Status: 429}), "upstream_websocket_http_429"},
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
			debug, err := openDebugOutput(testDiagnostics(new(evidence.Service), new(transportcapture.Service)), true, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = debug.Close() })
			ctx := requestexecution.WithDebug(t.Context(), debug)
			request, err := codexresponses.ParseRequest(mustTestJSON(t, titleRequestFields()))
			if err != nil {
				t.Fatal(err)
			}
			issues := testCriticalErrors()
			provider := &serverFakeProvider{results: []serverForwardResult{{err: test.err}}}
			err = executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, io.Discard, issues, nil)
			if !errors.Is(err, test.err) {
				t.Fatalf("original cause lost: %v", err)
			}
			data, err := os.ReadFile(debug.Paths()[0])
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
			notices := strings.Join(issues.Notices.Pending(), "\n")
			if rejection, ok := errors.AsType[*inferencegateway.WebSocketStatusError](test.err); ok {
				if record["upstream_status"] != float64(rejection.Status) {
					t.Fatalf("handshake status lost: %v", record)
				}
				guidance := "credentials"
				if rejection.Status == 429 {
					guidance = "Wait before retrying"
				}
				if !strings.Contains(notices, guidance) {
					t.Fatalf("missing handshake guidance: %s", notices)
				}
			}
			if !strings.Contains(notices, reference) {
				t.Fatalf("notice/reference mismatch: %s", notices)
			}
			loggedError, _ := record["error"].(string)
			if !strings.Contains(notices, loggedError) || !strings.Contains(loggedError, test.err.Error()) {
				t.Fatal("complete wrapped error missing from notice or debug event")
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
			debug, err := openDebugOutput(testDiagnostics(new(evidence.Service), new(transportcapture.Service)), true, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = debug.Close() })
			ctx := requestexecution.WithDebug(t.Context(), debug)
			request, err := codexresponses.ParseRequest(mustTestJSON(t, titleRequestFields()))
			if err != nil {
				t.Fatal(err)
			}
			issues := testCriticalErrors()
			provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(test.body)),
			}}}}
			_ = executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, test.output, issues, nil)
			data, err := os.ReadFile(debug.Paths()[0])
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var record map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
				t.Fatal(err)
			}
			notices := strings.Join(issues.Notices.Pending(), "\n")
			reference, _ := record["diagnostic_reference"].(string)
			if record["diagnostic_code"] != test.code || len(reference) != 12 || !strings.Contains(notices, reference) ||
				len(issues.Notices.Pending()) != 1 {
				t.Fatalf("diagnostics disagree: %v, %s", record, notices)
			}
			if test.code == "downstream_response_write" &&
				(!strings.Contains(notices, "private write failure") || record["error"] == nil) {
				t.Fatal("downstream write error missing from diagnostics")
			}
		})
	}
}

func testCriticalErrors() *requestexecution.Failures {
	return requestexecution.NewFailures(new(notice.Service))
}
