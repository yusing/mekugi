package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestProviderErrorEventIsFailedNotMissingTerminal(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"code":"server_error","message":"private provider message"}}`,
		`{"type":"error","status":429,"error":{"code":"rate_limit_exceeded","message":"private provider message"}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			response := serverHTTPResponse("data: " + payload + "\n\n")
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			issues := testCriticalErrors()
			var output bytes.Buffer
			err := executeRequest(t.Context(), t.Context(),
				serverRequest(t, func(request map[string]any) { request["stream"] = true }),
				http.Header{}, "error-session", provider, &output, issues, nil)
			if err != nil {
				t.Fatalf("explicit provider error became a router error: %v", err)
			}
			if !strings.Contains(output.String(), payload) {
				t.Fatalf("provider event was not preserved: %s", output.String())
			}
			notices := issues.Notices.Pending()
			if len(notices) != 1 || !strings.Contains(notices[0], "private provider message") ||
				strings.Contains(notices[0], "without a completed") {
				t.Fatalf("wrong failure notice: %v", notices)
			}
		})
	}
}

func TestGrokEncryptedHistoryCompatibility(t *testing.T) {
	for _, input := range []string{
		`[{"type":"reasoning","encrypted_content":"private"}]`,
		`[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private"}]}]`,
	} {
		t.Run(input, func(t *testing.T) {
			body := []byte(`{"model":"grok:grok-4.6","input":` + input + `}`)
			// No auth or HTTP client: rejection must happen before either is used.
			client := &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics()}
			_, err := client.ForwardExecution(t.Context(), t.Context(), body, grokTestHeaders())
			err = fmt.Errorf("execute request: %w", requestexecution.ForwardError(err))
			compatibility, ok := errors.AsType[*inferencegateway.CompatibilityError](err)
			if !ok || compatibility.Code != "grok_encrypted_history" {
				t.Fatalf("wrong classification: %v", err)
			}
			issues := testCriticalErrors()
			finalization := issues.Report(requestexecution.Failure{Session: "grok-session", Phase: "forward"}, err)
			if finalization.Code != "grok_encrypted_history" ||
				strings.Contains(strings.Join(issues.Notices.Pending(), ""), "transport") ||
				!strings.Contains(strings.Join(issues.Notices.Pending(), ""), "fresh") {
				t.Fatalf("wrong diagnostic: %+v, %v", finalization, issues.Notices.Pending())
			}

			provider := &serverFakeProvider{results: []serverForwardResult{{err: err}}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			testResponsesHandler(t.Context(), time.Second, provider, nil, nil)(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("compatibility rejection is retryable: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestStreamDiagnosticsProviderErrorCodes(t *testing.T) {
	for _, test := range []struct {
		payload string
		want    string
	}{
		{`{"type":"error","status":429,"error":{"code":"rate_limit_exceeded","message":"private"}}`, "rate_limit_exceeded"},
		{`{"type":"error","code":"invalid_encrypted_content","message":"private"}`, "invalid_encrypted_content"},
		{`{"type":"response.failed","response":{"error":{"code":"server_error","message":"private"}}}`, "server_error"},
		{`{"type":"error","error":{"code":"private_identifier","message":"private"}}`, "other"},
		{`{"type":"error","error":{"message":"private"}}`, "other"},
	} {
		d := &transportcapture.Stream{}
		d.Observe([]byte(test.payload))
		encoded, err := json.Marshal(d.Snapshot())
		if err != nil || d.ProviderErrorCode != test.want || strings.Contains(string(encoded), "private") {
			t.Fatalf("unsafe or missing code: %s, %v", encoded, err)
		}
	}
}

func TestResponsesWebSocketProviderErrorRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for _, event := range []any{
			map[string]any{"type": "error", "status": 429, "error": map[string]string{"code": "rate_limit_exceeded", "message": "private"}},
			socketEvent("response.completed", "recovered"),
		} {
			if _, err := providerSocketRead(ctx, conn); err != nil {
				t.Error(err)
				return
			}
			if err := providerSocketWrite(ctx, conn, event); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer upstream.Close()
	issues := testCriticalErrors()
	endpoint := testWebSocketEndpoint(ctx, time.Second, newTestProviderClient(upstream.URL, upstream.Client()), issues, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	command := map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{}}
	socketWrite(t, ctx, conn, command)
	event := socketRead(t, ctx, conn)
	if responses.StringField(event, "type") != "error" || string(event["status"]) != "429" {
		t.Fatalf("provider error changed: %s", responses.MustMarshalJSON(event))
	}
	socketWrite(t, ctx, conn, command)
	// A missing-terminal error would produce a second synthetic error instead.
	event = socketRead(t, ctx, conn)
	if responses.StringField(event, "type") == "error" {
		t.Fatalf("router emitted a duplicate error: %s", responses.MustMarshalJSON(event))
	}
	for responses.StringField(event, "type") != "response.completed" {
		event = socketRead(t, ctx, conn)
	}
	conn.CloseNow()
	endpoint.Close()
	notices := issues.Notices.Pending()
	if len(notices) != 1 || !strings.Contains(notices[0], "HTTP 429: rate_limit_exceeded: private") {
		t.Fatalf("lost actual WebSocket error: %v", notices)
	}
	for _, notice := range notices {
		if strings.Contains(notice, "invalid") || strings.Contains(notice, "without a completed") {
			t.Fatalf("misclassified provider error: %s", notice)
		}
	}
}

func TestResponsesWebSocketGrokEncryptedHistoryStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	provider := newTestProviderClient("http://unused.invalid", nil)
	provider.Grok = &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics()}
	endpoint := testWebSocketEndpoint(ctx, time.Second, provider, nil, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: grokTestHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": grokModel,
		"input": []any{map[string]string{"type": "reasoning", "encrypted_content": "private"}}})
	event := socketRead(t, ctx, conn)
	if responses.StringField(event, "type") != "error" || string(event["status"]) != "400" ||
		!strings.Contains(string(event["error"]), "grok_encrypted_history") {
		t.Fatalf("incorrect compatibility error: %s", responses.MustMarshalJSON(event))
	}
}
