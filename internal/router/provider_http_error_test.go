package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
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
		{"redacted", `{"error":{"message":"key provider-secret caller-secret account-secret"}}`, "key [redacted] [redacted] [redacted]"},
		{"controls", `{"error":{"message":"before\u001bafter\u202eend"}}`, "before after end"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newProviderHTTPError("OpenCode Go", 403, []byte(test.body),
				http.Header{"Authorization": {"Bearer provider-secret"}},
				http.Header{"Authorization": {"Bearer caller-secret"}, http.CanonicalHeaderKey(chatGPTAccountIDHeader): {"account-secret"}})
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("missing detail: %v", err)
			}
			diagnostic, ok := errors.AsType[*criticalDiagnosticError](forwardCriticalDiagnostic(err))
			if !ok || diagnostic.code != "upstream_provider_http_403" || strings.Contains(diagnostic.summary, test.want) {
				t.Fatalf("unsafe diagnostic: %#v", diagnostic)
			}
			issues := NewCriticalErrors()
			finalization := &requestFinalization{sessionID: "provider", upstreamStatusCode: 403,
				failurePhase: requestFailureForward, observation: requestObservation{outcome: requestOutcomeFailed}}
			issues.record(finalization, diagnostic)
			notices := issues.Pending()
			if len(notices) != 1 || !strings.Contains(notices[0], test.want) || strings.Contains(notices[0], "authentication") {
				t.Fatalf("provider detail hidden: %v", notices)
			}
		})
	}
	err := newProviderHTTPError("Grok", 500, []byte(strings.Repeat("界", 3000)))
	if !strings.HasSuffix(err.Error(), " [truncated]") || len([]rune(err.Error())) > 2100 {
		t.Fatalf("unbounded detail: %d", len([]rune(err.Error())))
	}
}

func TestOpenCodeHTTPErrorDelivery(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "provider-secret"}}).services()[0]
			calls := 0
			provider := newProviderClient("http://unused.invalid", nil)
			provider.opencode = map[string]*grokClient{service.prefix: {
				openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
						`{"error":{"type":"provider_failure","message":"Useful provider detail provider-secret"}}`))}, nil
				})},
			}}
			issues := NewCriticalErrors()
			body := openCodeTestRequest(t, service, false)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			req.Header = grokTestHeaders()
			recorder := httptest.NewRecorder()
			responsesHandler(ctx, time.Second, provider, issues, nil, nil)(recorder, req)
			if recorder.Code != status || recorder.Header().Get("Content-Type") != "application/json" ||
				!strings.Contains(recorder.Body.String(), "Useful provider detail [redacted]") || calls != 1 {
				t.Fatalf("HTTP error lost: %d %s (calls %d)", recorder.Code, recorder.Body.String(), calls)
			}
			endpoint := responsesWebSocketHandler(ctx, time.Second, provider, issues, nil, nil)
			defer endpoint.Close()
			server := httptest.NewServer(endpoint)
			defer server.Close()
			conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: grokTestHeaders()})
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
			if string(event["status"]) != fmt.Sprint(status) || !strings.Contains(string(event["error"]), "Useful provider detail [redacted]") {
				t.Fatalf("WebSocket error lost: %s", mustMarshalJSON(event))
			}
			conn.CloseNow()
			endpoint.Close()
			if calls != 2 {
				t.Fatalf("unexpected provider retries: %d", calls)
			}
			if notices := strings.Join(issues.Pending(), "\n"); !strings.Contains(notices, "Useful provider detail [redacted]") || strings.Contains(notices, "provider-secret") {
				t.Fatalf("notice missing or unsafe: %s", notices)
			}
		})
	}
}

type providerErrorBody struct {
	io.Reader
	closed bool
}

func (b *providerErrorBody) Close() error { b.closed = true; return nil }

func TestOpenCodeHTTPErrorBodyLimit(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	body := &providerErrorBody{Reader: strings.NewReader(strings.Repeat("x", maxUpstreamErrorDetailBytes+100))}
	client := &grokClient{openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: body}, nil
	})}}
	_, err := client.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders())
	if !body.closed || err == nil || !strings.Contains(err.Error(), "8 KiB limit") {
		t.Fatalf("unbounded or unclosed error body: %v, closed %v", err, body.closed)
	}
}

func TestProviderHTTPErrorNoticesStayDistinct(t *testing.T) {
	issues := NewCriticalErrors()
	for _, detail := range []string{"first failure", "second failure", "first failure"} {
		err := newProviderHTTPError("OpenCode Go", 403, []byte(detail))
		issues.record(&requestFinalization{sessionID: "same", upstreamStatusCode: 403,
			failurePhase: requestFailureForward, observation: requestObservation{outcome: requestOutcomeFailed}},
			forwardCriticalDiagnostic(err))
	}
	notices := issues.Pending()
	if len(notices) != 2 || !strings.Contains(notices[0], "first failure") ||
		!strings.Contains(notices[0], "2 times") || !strings.Contains(notices[1], "second failure") {
		t.Fatalf("distinct errors hidden: %v", notices)
	}
}

func TestOpenCodeHTTPErrorReadFailure(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	body := &providerErrorBody{Reader: iotest.ErrReader(errors.New("private transport detail"))}
	client := &grokClient{openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: body}, nil
	})}}
	_, err := client.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders())
	if !body.closed || err == nil || !strings.Contains(err.Error(), "could not be read") || strings.Contains(err.Error(), "private") {
		t.Fatalf("read failure hidden or unsafe: %v", err)
	}
}

func TestOpenCodeHTTPErrorStalledBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
		reader, writer := io.Pipe()
		defer writer.Close()
		client := &grokClient{openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 403, Header: http.Header{}, Body: reader}, nil
		})}}
		start := time.Now()
		_, err := client.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders())
		if err == nil || !strings.Contains(err.Error(), "could not be read") || time.Since(start) != 5*time.Second {
			t.Fatalf("error read not bounded: %v, elapsed %s", err, time.Since(start))
		}
	})
}

func TestOpenCodeHTTPErrorReadCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
		reader, writer := io.Pipe()
		defer writer.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client := &grokClient{openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			context.AfterFunc(request.Context(), func() { _ = writer.CloseWithError(request.Context().Err()) })
			return &http.Response{StatusCode: 403, Header: http.Header{}, Body: reader}, nil
		})}}
		go func() {
			time.Sleep(time.Second)
			cancel()
		}()
		start := time.Now()
		_, err := client.forwardExecution(ctx, t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders())
		if !errors.Is(err, context.Canceled) || time.Since(start) != time.Second {
			t.Fatalf("cancellation lost: %v, elapsed %s", err, time.Since(start))
		}
	})
}

func TestProviderHTTPErrorCredentialSchemes(t *testing.T) {
	for _, scheme := range []string{"Bearer", "bearer", "bEaReR"} {
		err := newProviderHTTPError("OpenCode", 403, []byte(`{"error":{"message":"caller-secret provider-secret"}}`),
			http.Header{"Authorization": {scheme + " caller-secret"}, "X-Api-Key": {"provider-secret"}})
		if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "[redacted] [redacted]") {
			t.Fatalf("credential escaped redaction for %s: %v", scheme, err)
		}
	}
}
