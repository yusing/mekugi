package launch

import (
	"bytes"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestProviderHTTPErrorDetails(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"region", `{"error":{"name":"RegionError","message":"Enable China-hosted models in the console"}}`, "RegionError: Enable China-hosted models in the console"},
		{"quota", `{"error":{"type":"quota_error","message":"Your credits are exhausted"}}`, "quota_error: Your credits are exhausted"},
		{"code", `{"error":{"code":"model_unavailable","message":"Choose another model"}}`, "model_unavailable: Choose another model"},
		{"string", `{"error":"Model is overloaded"}`, "Model is overloaded"},
		{"top level", `{"name":"AccessError","message":"Workspace disabled"}`, "AccessError: Workspace disabled"},
		{"plain", "Service maintenance", "Service maintenance"},
		{"empty", "", "Forbidden"},
		{"unredacted", `{"error":{"message":"key provider-secret caller-secret account-secret"}}`, "key provider-secret caller-secret account-secret"},
		{"controls", `{"error":{"message":"before\u001bafter\u202eend"}}`, "before\x1bafter\u202eend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := inferencegateway.NewProviderHTTPError("Custom provider", 403, []byte(test.body))
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("missing detail: %v", err)
			}
			diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](requestexecution.ForwardError(err))
			if !ok || diagnostic.Code != "upstream_provider_http_403" || strings.Contains(diagnostic.Summary, test.want) {
				t.Fatalf("unsafe diagnostic: %#v", diagnostic)
			}
			issues := testCriticalErrors()
			issues.Report(requestexecution.Failure{Session: "provider", UpstreamStatus: 403,
				Phase: "forward"}, diagnostic)
			notices := issues.Notices.Pending()
			if len(notices) != 1 || !strings.Contains(notices[0], test.want) || strings.Contains(notices[0], "authentication") {
				t.Fatalf("provider detail hidden: %v", notices)
			}
		})
	}
	err := inferencegateway.NewProviderHTTPError("Custom provider", 500, []byte(strings.Repeat("界", 3000)))
	if !strings.HasSuffix(err.Error(), strings.Repeat("界", 3000)) {
		t.Fatalf("provider detail was truncated: %d", len([]rune(err.Error())))
	}
}

func TestCustomProviderHTTPErrorDelivery(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			calls := 0
			provider := customTestProvider(&http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
					`{"error":{"type":"provider_failure","message":"Useful provider detail provider-secret"}}`))}, nil
			})})
			issues := testCriticalErrors()
			body := responses.MustMarshalJSON(serverRequest(t, nil).Fields)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header = codexAuthHeaders()
			recorder := httptest.NewRecorder()
			testResponsesHandler(ctx, time.Second, provider, issues, nil)(recorder, req)
			if recorder.Code != status || recorder.Header().Get("Content-Type") != "application/json" ||
				!strings.Contains(recorder.Body.String(), "Useful provider detail provider-secret") || calls != 1 {
				t.Fatalf("HTTP error lost: %d %s (calls %d)", recorder.Code, recorder.Body.String(), calls)
			}
			endpoint := testWebSocketEndpoint(ctx, time.Second, provider, issues, nil)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			var command map[string]any
			if err := json.Unmarshal(body, &command); err != nil {
				t.Fatal(err)
			}
			command["type"] = "response.create"
			socketWrite(t, ctx, conn, command)
			event := socketRead(t, ctx, conn)
			if string(event["status"]) != fmt.Sprint(status) || !strings.Contains(string(event["error"]), "Useful provider detail provider-secret") {
				t.Fatalf("WebSocket error lost: %s", responses.MustMarshalJSON(event))
			}
			conn.CloseNow()
			endpoint.Close()
			if calls != 2 {
				t.Fatalf("unexpected provider retries: %d", calls)
			}
			if notices := strings.Join(issues.Notices.Pending(), "\n"); !strings.Contains(notices, "Useful provider detail provider-secret") {
				t.Fatalf("notice missing complete detail: %s", notices)
			}
		})
	}
}

func TestProviderHTTPErrorNoticesStayDistinct(t *testing.T) {
	issues := testCriticalErrors()
	for _, detail := range []string{"first failure", "second failure", "first failure"} {
		err := inferencegateway.NewProviderHTTPError("Custom provider", 403, []byte(detail))
		issues.Report(requestexecution.Failure{Session: "same", UpstreamStatus: 403,
			Phase: "forward"},
			requestexecution.ForwardError(err))
	}
	notices := issues.Notices.Pending()
	if len(notices) != 2 || !strings.Contains(notices[0], "first failure") ||
		!strings.Contains(notices[0], "2 times") || !strings.Contains(notices[1], "second failure") {
		t.Fatalf("distinct errors hidden: %v", notices)
	}
}
