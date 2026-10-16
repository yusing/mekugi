package router

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/providers/opencode"
)

// Cover streamed and non-streamed provider failures through request delivery.
func TestTerminalProviderErrorNotices(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		stream              bool
	}{
		{"websocket_error", `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"Unsupported service_tier: fast"}}`, "HTTP 400: invalid_request_error: Unsupported service_tier: fast", true},
		{"flat_error", `{"type":"error","code":"invalid_request_error","message":"Unsupported service_tier: fast"}`, "invalid_request_error: Unsupported service_tier: fast", true},
		{"failed_event", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"provider detail"}}}`, "server_error: provider detail", true},
		{"failed_response", `{"status":"failed","error":{"code":"server_error","message":"provider detail"}}`, "server_error: provider detail", false},
		{"string_error", `{"type":"error","error":"provider detail"}`, "provider detail", true},
		{"missing_error", `{"type":"response.failed","response":{"status":"failed"}}`, "terminal state failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.payload
			if tc.stream {
				body = "data: " + body + "\n\n"
			}
			response := serverHTTPResponse(body)
			if tc.stream {
				response.Header.Set("Content-Type", "text/event-stream")
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			issues := testCriticalErrors()
			var output bytes.Buffer
			err := executeRequest(t.Context(), t.Context(),
				serverRequest(t, func(fields map[string]any) { fields["stream"] = tc.stream }),
				http.Header{}, "terminal-detail", provider, &output, issues, nil)
			if err != nil {
				t.Fatal(err)
			}
			notices := issues.Notices.Pending()
			if len(notices) != 1 || !strings.Contains(notices[0], tc.want) {
				t.Fatalf("notices=%q want=%q", notices, tc.want)
			}
			if !strings.Contains(output.String(), tc.payload) {
				t.Fatalf("provider output changed: %s", output.String())
			}
		})
	}
}

func TestTerminalProviderErrorCompletenessAndCaptureIsolation(t *testing.T) {
	payload := []byte(`{"type":"error","status":400,"error":{"message":"actual detail credential-secret\u001b\n"}}`)
	err := terminalProviderError(payload, true)
	if err == nil || !strings.Contains(err.Error(), "actual detail credential-secret\x1b\n") {
		t.Fatalf("incomplete provider error: %v", err)
	}
	diagnostics := &transportcapture.Stream{}
	diagnostics.Observe(payload)
	encoded, marshalErr := json.Marshal(diagnostics.Snapshot())
	if marshalErr != nil || strings.Contains(string(encoded), "actual detail") || strings.Contains(string(encoded), "credential-secret") {
		t.Fatalf("provider details leaked to diagnostics: %s %v", encoded, marshalErr)
	}
	issues := testCriticalErrors()
	for _, message := range []string{"first cause", "second cause", "first cause"} {
		body := []byte(`{"type":"error","error":{"message":"` + message + `"}}`)
		issues.record(&requestFinalization{
			sessionID: "same", failurePhase: requestFailureTerminalValidation,
			upstreamTerminalState: responseevents.TerminalFailed,
			observation:           requestObservation{outcome: requestOutcomeFailed},
			providerFailure:       terminalProviderError(body, true),
		}, nil)
	}
	notices := issues.Notices.Pending()
	if len(notices) != 2 || !strings.Contains(notices[0], "first cause") ||
		!strings.Contains(notices[0], "occurred 2 times") || !strings.Contains(notices[1], "second cause") {
		t.Fatalf("distinct causes were merged: %q", notices)
	}
	body, _ := json.Marshal(map[string]any{"type": "error", "message": strings.Repeat("x", 3000)})
	if err := terminalProviderError(body, true); err == nil || !strings.Contains(err.Error(), strings.Repeat("x", 3000)) {
		t.Fatalf("provider error truncated: %v", err)
	}
}

func TestAdaptedProviderErrorDelivery(t *testing.T) {
	for _, format := range []string{"chat", "responses", "anthropic"} {
		for _, stream := range []bool{false, true} {
			t.Run(format+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "provider-secret"}}).Services()[0]
				service.Catalog = openCodeTestService(t, map[string]opencode.Metadata{"test": {Format: format}}).Catalog
				payload := `{"type":"error","error":{"type":"invalid_request_error","message":"actual rejection provider-secret caller-secret"}}`
				if format == "responses" {
					payload = `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request_error","message":"actual rejection provider-secret caller-secret"}}}`
				}
				provider := newProviderClient("http://unused.invalid", nil)
				provider.opencode = map[string]*grokClient{service.Prefix: {
					openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
							Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}, nil
					})},
				}}
				body, _ := json.Marshal(map[string]any{"model": "opencode-go:test", "input": []any{}, "stream": stream})
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				req.Header = grokTestHeaders()
				req.Header.Set("Authorization", "Bearer caller-secret")
				issues := testCriticalErrors()
				recorder := httptest.NewRecorder()
				responsesHandler(t.Context(), 5*time.Second, provider, issues, nil)(recorder, req)
				for _, text := range []string{recorder.Body.String(), strings.Join(issues.Notices.Pending(), "\n")} {
					if !strings.Contains(text, "actual rejection provider-secret caller-secret") {
						t.Fatalf("incomplete adapted error: %s", text)
					}
				}
				if stream && (!strings.Contains(recorder.Body.String(), "response.failed") || strings.Contains(recorder.Body.String(), "response.completed")) {
					t.Fatalf("incorrect terminal delivery: %s", recorder.Body.String())
				}
			})
		}
	}
}
