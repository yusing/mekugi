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

	"github.com/coder/websocket"
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
			issues := NewCriticalErrors()
			var output bytes.Buffer
			err := executeRequest(t.Context(), t.Context(),
				serverRequest(t, func(request map[string]any) { request["stream"] = true }),
				http.Header{}, "error-session", provider, &output, issues, nil, nil)
			if err != nil {
				t.Fatalf("explicit provider error became a router error: %v", err)
			}
			if !strings.Contains(output.String(), payload) {
				t.Fatalf("provider event was not preserved: %s", output.String())
			}
			notices := issues.Pending()
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
			client := &grokClient{}
			_, err := client.forwardExecution(t.Context(), t.Context(), body, grokTestHeaders())
			err = fmt.Errorf("execute request: %w", forwardCriticalDiagnostic(err))
			compatibility, ok := errors.AsType[*requestCompatibilityError](err)
			if !ok || compatibility.code != "grok_encrypted_history" {
				t.Fatalf("wrong classification: %v", err)
			}
			issues := NewCriticalErrors()
			finalization := &requestFinalization{sessionID: "grok-session", failurePhase: requestFailureForward,
				observation: requestObservation{outcome: requestOutcomeFailed}}
			issues.record(finalization, err)
			if finalization.diagnosticCode != "grok_encrypted_history" ||
				strings.Contains(strings.Join(issues.Pending(), ""), "transport") ||
				!strings.Contains(strings.Join(issues.Pending(), ""), "fresh") {
				t.Fatalf("wrong diagnostic: %+v, %v", finalization, issues.Pending())
			}

			provider := &serverFakeProvider{results: []serverForwardResult{{err: err}}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			responsesHandler(t.Context(), time.Second, provider, nil, nil, nil)(recorder, request)
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
		d := &streamDiagnostics{}
		d.observe([]byte(test.payload))
		encoded, err := json.Marshal(d.snapshot())
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
	issues := NewCriticalErrors()
	endpoint := responsesWebSocketHandler(ctx, time.Second, newProviderClient(upstream.URL, upstream.Client()), issues, nil, nil)
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
	if jsonString(event, "type") != "error" || string(event["status"]) != "429" {
		t.Fatalf("provider error changed: %s", mustMarshalJSON(event))
	}
	socketWrite(t, ctx, conn, command)
	// A missing-terminal error would produce a second synthetic error instead.
	event = socketRead(t, ctx, conn)
	if jsonString(event, "type") == "error" {
		t.Fatalf("router emitted a duplicate error: %s", mustMarshalJSON(event))
	}
	for jsonString(event, "type") != "response.completed" {
		event = socketRead(t, ctx, conn)
	}
	conn.CloseNow()
	endpoint.Close()
	issues.mu.Lock()
	defer issues.mu.Unlock()
	if len(issues.entries) != 1 || !strings.Contains(issues.entries[0].message, "HTTP 429: rate_limit_exceeded: private") {
		t.Fatalf("lost actual WebSocket error: %v", issues.entries)
	}
	for _, notice := range issues.entries {
		if strings.Contains(notice.message, "invalid") || strings.Contains(notice.message, "without a completed") {
			t.Fatalf("misclassified provider error: %s", notice.message)
		}
	}
}

func TestResponsesWebSocketGrokEncryptedHistoryStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	provider := newProviderClient("http://unused.invalid", nil)
	provider.grok = &grokClient{}
	endpoint := responsesWebSocketHandler(ctx, time.Second, provider, nil, nil, nil)
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
	if jsonString(event, "type") != "error" || string(event["status"]) != "400" ||
		!strings.Contains(string(event["error"]), "grok_encrypted_history") {
		t.Fatalf("incorrect compatibility error: %s", mustMarshalJSON(event))
	}
}
