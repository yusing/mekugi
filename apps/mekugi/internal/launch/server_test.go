package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/openai"
)

type serverForwardResult struct {
	response *http.Response
	err      error
}

type serverFakeProvider struct {
	results           []serverForwardResult
	forwarded         [][]byte
	forwardedHeaders  []http.Header
	forwardedCacheKey []string
}

func (f *serverFakeProvider) ForwardExecution(_, _ context.Context, body []byte, headers http.Header, cacheKey string) (*inferencegateway.Response, error) {
	f.forwardedHeaders = append(f.forwardedHeaders, headers.Clone())
	f.forwardedCacheKey = append(f.forwardedCacheKey, cacheKey)
	return testGatewayResponse(f.next(body))
}

func (f *serverFakeProvider) next(body []byte) (*http.Response, error) {
	f.forwarded = append(f.forwarded, bytes.Clone(body))
	if len(f.results) == 0 {
		return nil, errors.New("unexpected upstream forward")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.response, result.err
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func serverRequest(t *testing.T, mutate func(map[string]any)) codexresponses.Request {
	t.Helper()
	request := map[string]any{
		"model":     "gpt-test",
		"reasoning": map[string]any{"effort": "high"},
		"input": []any{
			testFlatCodeModeAdditionalTools(testCodeModeDescription),
			map[string]any{"role": "user", "content": "task"},
		},
		"tools":       []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice": "auto",
	}
	if mutate != nil {
		mutate(request)
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func serverMetadataHeaders(t *testing.T, requestKind string, workspaces map[string]json.RawMessage) http.Header {
	t.Helper()
	encoded, err := json.Marshal(codexresponses.TurnMetadata{RequestKind: responses.RequestKind(requestKind), Directories: workspaces})
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set(codexresponses.TurnMetadataHeader, string(encoded))
	headers.Set(codexresponses.ThreadIDHeader, "thread-1")
	return headers
}

func serverCompactionMetadataHeaders(t *testing.T) http.Header {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"request_kind": "compaction",
		"turn_id":      "turn-1",
		"compaction": map[string]any{
			"trigger": "auto", "reason": "context_limit", "implementation": "responses",
			"phase": "standalone_turn", "strategy": "memento",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set(codexresponses.TurnMetadataHeader, string(encoded))
	return headers
}

func TestExecuteRequestFailsClosedBeforeUpstreamWhenRewriteIsIneligible(t *testing.T) {
	workspace := t.TempDir()
	validHeaders := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
	missingThreadHeaders := validHeaders.Clone()
	missingThreadHeaders.Del(codexresponses.ThreadIDHeader)
	tests := []struct {
		name      string
		sessionID string
		headers   http.Header
		mutate    func(map[string]any)
		want      string
	}{
		{name: "missing session", headers: validHeaders, want: "valid session ID"},
		{name: "missing thread", sessionID: "session", headers: missingThreadHeaders, want: "valid Codex thread ID"},
		{name: "invalid metadata", sessionID: "session", headers: http.Header{}, want: "valid turn metadata"},
		{name: "unknown request kind", sessionID: "session", headers: serverMetadataHeaders(t, "other", nil), want: "valid turn metadata"},
		{name: "legacy compact request kind", sessionID: "session", headers: serverMetadataHeaders(t, "compact", nil), want: "valid turn metadata"},
		{name: "compaction metadata on ordinary turn", sessionID: "session", headers: serverCompactionMetadataHeaders(t), mutate: func(request map[string]any) {
			delete(request, "tools")
			request["stream"] = true
			request["parallel_tool_calls"] = false
		}, want: "compaction request cannot expose tools"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &serverFakeProvider{}
			err := executeRequest(t.Context(), t.Context(), serverRequest(t, test.mutate), test.headers, test.sessionID, provider, io.Discard, nil, newManagedMekugiProxy(t))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if len(provider.forwarded) != 0 {
				t.Fatalf("ineligible request reached upstream: %s", provider.forwarded[0])
			}
		})
	}
}

func TestExecuteRequestDoesNotRequireWorkspaceMetadata(t *testing.T) {
	tests := []struct {
		name        string
		directories map[string]json.RawMessage
	}{
		{name: "omitted"},
		{name: "empty", directories: map[string]json.RawMessage{}},
		{name: "unusable", directories: map[string]json.RawMessage{"/missing": nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseBody := string(mustTestJSON(t, map[string]any{"status": "completed"}))
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
			proxy := newManagedMekugiProxy(t)

			var output bytes.Buffer
			err := executeRequest(
				t.Context(),
				t.Context(),
				serverRequest(t, nil),
				serverMetadataHeaders(t, "turn", test.directories),
				"session",
				provider,
				&output,
				nil,
				proxy,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 {
				t.Fatalf("upstream requests = %d, want 1", len(provider.forwarded))
			}
		})
	}
}

func TestExecuteRequestForwardsCompactionWithoutRouterRewrite(t *testing.T) {
	repeated := strings.Repeat("exact compaction text with a reserved !V prefix and repeated content; ", 20)
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model":           "gpt-test",
		"access_programs": map[string]string{"cyber": "standard"},
		"instructions":    "Caller-owned compaction instructions.",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{}},
			map[string]any{"type": "message", "role": "developer", "content": "instructions"},
			map[string]any{"type": "message", "role": "user", "content": repeated},
		},
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"stream":              true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	originalBody, err := json.Marshal(parsed.Fields)
	if err != nil {
		t.Fatal(err)
	}
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{"status": "completed", "output": []any{
			map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": "native response"},
			}},
		}},
	})
	responseBody := "event: response.completed\ndata: " + string(completed) + "\n\n"
	response := serverHTTPResponse(responseBody)
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	proxy := newManagedMekugiProxy(t)
	proxy.activity.Observe("root", "", "/root", false)
	headers := serverCompactionMetadataHeaders(t)
	var compactMetadata map[string]any
	for name, values := range headers {
		if err := json.Unmarshal([]byte(values[0]), &compactMetadata); err != nil {
			t.Fatal(err)
		}
		delete(headers, name)
	}
	compactMetadata["thread_id"], compactMetadata["parent_thread_id"] = "agent-thread", "root"
	compactMetadata["agent_name"], compactMetadata["subagent_kind"] = "/root/probe", "thread_spawn"
	headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, compactMetadata)))
	headers.Set(codexresponses.ThreadIDHeader, "agent-thread")

	var output bytes.Buffer
	err = executeRequest(t.Context(), t.Context(), parsed, headers, "session", provider, &output, nil, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(provider.forwarded))
	}
	if !bytes.Equal(provider.forwarded[0], originalBody) {
		t.Fatalf("compaction request was rewritten:\n got %s\nwant %s", provider.forwarded[0], originalBody)
	}
	if output.String() != responseBody {
		t.Fatalf("visible response = %s, want %s", output.String(), responseBody)
	}
	provider = &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	provider.results[0].response.Header.Set("Content-Type", "text/event-stream")
	output.Reset()
	if err := executeRequest(t.Context(), t.Context(), parsed, headers, "session", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], originalBody) || output.String() != responseBody {
		t.Fatalf("repeated compaction changed native forwarding or response: requests=%d output=%q", len(provider.forwarded), output.String())
	}
}

func TestExecuteRequestPassesThroughOriginalRequestAndRecordsUsage(t *testing.T) {
	parsed := serverRequest(t, func(request map[string]any) {
		request["prompt_cache_key"] = "control-cache"
		request["access_programs"] = map[string]string{"cyber": "standard"}
	})
	originalBody, err := json.Marshal(parsed.Fields)
	if err != nil {
		t.Fatal(err)
	}
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage": map[string]any{
			"input_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 5},
			"output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
		},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	var output bytes.Buffer
	issues := testCriticalErrors()
	if err := executeRequest(t.Context(), t.Context(), parsed, http.Header{}, "session", provider, &output, issues, nil); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], originalBody) {
		t.Fatalf("forwarded request = %q, want original %q", provider.forwarded, originalBody)
	}
	if got := provider.forwardedCacheKey[0]; got != "control-cache" {
		t.Fatalf("upstream cache key = %q, want control-cache", got)
	}
	if output.String() != responseBody {
		t.Fatalf("visible response = %q, want %q", output.String(), responseBody)
	}
	if len(issues.Notices.Pending()) != 0 {
		t.Fatal("successful request emitted critical notice")
	}
}

func TestExecuteRequestPreservesReasoningHeader(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, present := range []bool{false, true} {
			response := serverHTTPResponse(`{"status":"completed","output":[]}`)
			if stream {
				response = serverHTTPResponse("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				response.Header.Set("Content-Type", "text/event-stream")
			}
			if present {
				response.Header.Set("X-Reasoning-Included", "true")
			}
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			output := httptest.NewRecorder()
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
			if err := executeRequest(t.Context(), t.Context(), request, http.Header{}, "session", provider, output, nil, nil); err != nil {
				t.Fatal(err)
			}
			if got := output.Header().Get("X-Reasoning-Included"); (got == "true") != present {
				t.Fatalf("stream=%t present=%t: header=%q", stream, present, got)
			}
		}
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.

type serverErrorWriter struct{ err error }

func (w serverErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestExecuteRequestRecordsUsageAndFailureWhenDeliveryFails(t *testing.T) {
	workspace := t.TempDir()
	responseBody := string(mustTestJSON(t, map[string]any{
		"status": "completed",
		"usage":  map[string]any{"input_tokens": 10},
	}))
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(responseBody)}}}
	issues := testCriticalErrors()
	err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil}), "session", provider, serverErrorWriter{err: io.ErrClosedPipe}, issues, newManagedMekugiProxy(t))
	if err == nil {
		t.Fatal("delivery failure returned no error")
	}
}

type serverRepeatingReader struct{}

func (serverRepeatingReader) Read(content []byte) (int, error) {
	for index := range content {
		content[index] = 'x'
	}
	return len(content), nil
}

func TestResponsesHandlerRejectsBackgroundBeforeUpstream(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		strings.NewReader(`{"model":"gpt-test","input":"task","background":true}`),
	)
	recorder := httptest.NewRecorder()
	provider := &serverFakeProvider{}
	testResponsesHandler(t.Context(), time.Minute, provider, nil, nil)(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "background Responses requests are not supported") {
		t.Fatalf("response = %q", recorder.Body.String())
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("background request reached upstream")
	}
}

func TestResponsesHandlerRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", io.LimitReader(serverRepeatingReader{}, codexresponses.MaxRequestBytes+1))
	recorder := httptest.NewRecorder()
	provider := &serverFakeProvider{}

	testResponsesHandler(t.Context(), time.Minute, provider, nil, nil)(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("request beyond router buffer budget reached upstream")
	}
}

type serverProviderFunc func(context.Context, context.Context, []byte, http.Header, string) (*http.Response, error)

func (f serverProviderFunc) ForwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*inferencegateway.Response, error) {
	return testGatewayResponse(f(startCtx, responseCtx, body, headers, cacheKey))
}

type serverCancelableSSEBody struct {
	ctx      context.Context
	content  *strings.Reader
	canceled chan struct{}
	once     sync.Once
}

func (body *serverCancelableSSEBody) Read(content []byte) (int, error) {
	if body.content.Len() > 0 {
		return body.content.Read(content)
	}
	<-body.ctx.Done()
	body.once.Do(func() { close(body.canceled) })
	return 0, body.ctx.Err()
}

func (*serverCancelableSSEBody) Close() error { return nil }

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestResponsesHandlerDoesNotLogClientCancellationAsOperationalEvent(t *testing.T) {
	upstreamEvent := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	upstreamCanceled := make(chan struct{})
	provider := serverProviderFunc(func(_, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &serverCancelableSSEBody{
				ctx: responseCtx, content: strings.NewReader(upstreamEvent), canceled: upstreamCanceled,
			},
		}, nil
	})
	issues := testCriticalErrors()
	handler := testResponsesHandler(t.Context(), time.Minute, provider, issues, nil)
	handled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler(writer, request)
		close(handled)
	}))
	defer server.Close()

	body := mustTestJSON(t, map[string]any{
		"model": "gpt-test",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{}},
			map[string]any{"type": "message", "role": "developer", "content": "instructions"},
			map[string]any{"type": "message", "role": "user", "content": "summarize the conversation"},
		},
		"tool_choice": "auto", "parallel_tool_calls": false, "stream": true,
	})
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = serverCompactionMetadataHeaders(t)
	request.Header.Set(codexresponses.RoutingSessionHeader, "session")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	visible := make([]byte, len(upstreamEvent))
	if _, err := io.ReadFull(response.Body, visible); err != nil {
		t.Fatalf("read initial upstream event: %v", err)
	}
	if string(visible) != upstreamEvent {
		t.Fatalf("visible event = %q, want %q", visible, upstreamEvent)
	}
	cancelRequest()
	response.Body.Close()

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not stop after the client canceled its request")
	}
	select {
	case <-upstreamCanceled:
	default:
		t.Fatal("client cancellation did not reach the upstream response body")
	}
}

func TestExecuteRequestSuccessfulStreamLifecycle(t *testing.T) {
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"usage": map[string]any{
				"input_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 4},
				"output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		},
	})
	response := serverHTTPResponse("event: response.completed\ndata: " + string(completed) + "\n\n")
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	recorder := httptest.NewRecorder()
	writer := &inferencegateway.ResponseWriter{ResponseWriter: recorder}
	issues := testCriticalErrors()
	err := executeRequest(
		t.Context(), t.Context(),
		serverRequest(t, func(request map[string]any) { request["stream"] = true }),
		http.Header{}, "stream-session", provider, writer, issues, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

}

func TestExecuteRequestTerminalOutcomes(t *testing.T) {
	failed := mustTestJSON(t, map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"status": "failed",
			"usage":  map[string]any{"input_tokens": 9},
		},
	})
	failedResponse := serverHTTPResponse("event: response.failed\ndata: " + string(failed) + "\n\n")
	failedResponse.Header.Set("Content-Type", "text/event-stream")
	tests := []struct {
		name     string
		response *http.Response
		mutate   func(map[string]any)
	}{
		{
			name:     "upstream terminal failure",
			response: failedResponse,
			mutate:   func(request map[string]any) { request["stream"] = true },
		},
		{
			name: "non-2xx upstream response",
			response: &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`)),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &serverFakeProvider{results: []serverForwardResult{{response: test.response}}}
			issues := testCriticalErrors()
			err := executeRequest(
				t.Context(), t.Context(), serverRequest(t, test.mutate), http.Header{}, "session",
				provider, io.Discard, issues, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues.Notices.Pending()) != 1 {
				t.Fatal("terminal failure must queue one notice")
			}
		})
	}
}

func TestExecuteRequestCancellationBeforeResponseLifecycle(t *testing.T) {
	started := make(chan struct{})
	provider := serverProviderFunc(func(startCtx, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		<-startCtx.Done()
		return nil, startCtx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	issues := testCriticalErrors()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, ctx, serverRequest(t, nil), http.Header{}, "session", provider, io.Discard,
			issues, nil,
		)
	}()
	<-started
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}

}

type serverBlockingSSEBody struct {
	ctx     context.Context
	content *strings.Reader
	blocked chan struct{}
	once    sync.Once
}

func (body *serverBlockingSSEBody) Read(content []byte) (int, error) {
	if body.content.Len() > 0 {
		return body.content.Read(content)
	}
	body.once.Do(func() { close(body.blocked) })
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (*serverBlockingSSEBody) Close() error { return nil }

type serverBlockingWriter struct {
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *serverBlockingWriter) Write(content []byte) (int, error) {
	writer.once.Do(func() { close(writer.blocked) })
	<-writer.release
	return len(content), nil
}

func TestStreamIdleTimeoutPausesDuringDownstreamBackpressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		stream := httptransport.IdleRead(t.Context(), upstreamReader, time.Minute)
		defer func() { _ = stream.Close() }()
		defer func() { _ = upstreamWriter.Close() }()

		downstream := &serverBlockingWriter{blocked: make(chan struct{}), release: make(chan struct{})}
		type copyResult struct {
			state responses.TerminalState
			err   error
		}
		result := make(chan copyResult, 1)
		go func() {
			state, err := copySSETransformed(downstream, stream, nil, nil)
			result <- copyResult{state: state, err: err}
		}()

		if _, err := io.WriteString(upstreamWriter, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"); err != nil {
			t.Fatal(err)
		}
		<-downstream.blocked
		upstreamResult := make(chan error, 1)
		go func() {
			_, err := io.WriteString(upstreamWriter, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			upstreamResult <- err
		}()
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		close(downstream.release)
		if err := <-upstreamResult; err != nil {
			t.Fatal(err)
		}
		got := <-result
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.state != responses.TerminalCompleted {
			t.Fatalf("terminal state = %s, want completed", got.state)
		}
	})
}

func TestStreamIdleTimeoutResetsOnPartialSSEBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		stream := httptransport.IdleRead(t.Context(), upstreamReader, time.Minute)
		defer func() { _ = stream.Close() }()
		defer func() { _ = upstreamWriter.Close() }()

		type copyResult struct {
			state responses.TerminalState
			err   error
		}
		result := make(chan copyResult, 1)
		go func() {
			state, err := copySSETransformed(io.Discard, stream, nil, nil)
			result <- copyResult{state: state, err: err}
		}()

		for _, fragment := range []string{
			"event: response.",
			"completed\n",
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n",
			"\n",
		} {
			if _, err := io.WriteString(upstreamWriter, fragment); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			time.Sleep(45 * time.Second)
		}

		got := <-result
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.state != responses.TerminalCompleted {
			t.Fatalf("terminal state = %s, want completed", got.state)
		}
	})
}

func TestExecuteRequestStreamIdleTimeoutLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		upstreamReader, upstreamWriter := io.Pipe()
		defer func() { _ = upstreamWriter.Close() }()

		response := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       httptransport.IdleRead(t.Context(), upstreamReader, time.Minute),
		}
		provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
		recorder := httptest.NewRecorder()
		writer := &inferencegateway.ResponseWriter{ResponseWriter: recorder}
		issues := testCriticalErrors()
		result := make(chan error, 1)
		go func() {
			result <- executeRequest(
				t.Context(), t.Context(),
				serverRequest(t, func(request map[string]any) { request["stream"] = true }),
				http.Header{}, "idle-session", provider, writer, issues, nil,
			)
		}()

		if _, err := io.WriteString(upstreamWriter, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		err := <-result
		if !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
			t.Fatalf("error = %v, want stream idle timeout", err)
		}

	})
}

func TestCopySSETransformedKeepsMalformedStateInvalid(t *testing.T) {
	completed := mustTestJSON(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"status": "completed"},
	})
	body := "data: {not-json}\n\n" +
		"event: response.completed\n" +
		"data: " + string(completed) + "\n\n"
	state, err := copySSETransformed(io.Discard, strings.NewReader(body), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != responses.TerminalInvalid {
		t.Fatalf("terminal state = %v, want invalid", state)
	}

	for _, test := range []struct {
		name              string
		current, observed responses.TerminalState
	}{
		{name: "invalid then completed", current: responses.TerminalInvalid, observed: responses.TerminalCompleted},
		{name: "invalid then failed", current: responses.TerminalInvalid, observed: responses.TerminalFailed},
		{name: "completed then invalid", current: responses.TerminalCompleted, observed: responses.TerminalInvalid},
		{name: "failed then invalid", current: responses.TerminalFailed, observed: responses.TerminalInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := responses.MergeTerminal(test.current, test.observed); got != responses.TerminalInvalid {
				t.Fatalf("responses.MergeTerminal() = %v, want invalid", got)
			}
		})
	}
}

func TestExecuteRequestCancellationAfterResponseLifecycle(t *testing.T) {
	blocked := make(chan struct{})
	provider := serverProviderFunc(func(_, executionCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: httptransport.IdleRead(executionCtx, &serverBlockingSSEBody{
				ctx: executionCtx, content: strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"), blocked: blocked,
			}, time.Hour),
		}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	recorder := httptest.NewRecorder()
	writer := &inferencegateway.ResponseWriter{ResponseWriter: recorder}
	issues := testCriticalErrors()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, ctx,
			serverRequest(t, func(request map[string]any) { request["stream"] = true }),
			http.Header{}, "session", provider, writer, issues, nil,
		)
	}()
	<-blocked
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}

}

type serverCancelAfterTerminalResponseWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
	once   sync.Once
}

func (writer *serverCancelAfterTerminalResponseWriter) Write(content []byte) (int, error) {
	written, err := writer.ResponseWriter.Write(content)
	if bytes.Contains(content, []byte(`"type":"response.completed"`)) {
		writer.once.Do(writer.cancel)
	}
	return written, err
}

func (writer *serverCancelAfterTerminalResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func TestExecuteRequestCompletesAtTerminalEventBeforeStreamEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	blocked := make(chan struct{})
	completed := mustTestJSON(t, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"usage":  map[string]any{"input_tokens": 7},
		},
	})
	provider := serverProviderFunc(func(_, executionCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &serverBlockingSSEBody{
				ctx: executionCtx, content: strings.NewReader("event: response.completed\ndata: " + string(completed) + "\n\n"), blocked: blocked,
			},
		}, nil
	})
	recorder := httptest.NewRecorder()
	downstream := &serverCancelAfterTerminalResponseWriter{ResponseWriter: recorder, cancel: cancel}
	writer := &inferencegateway.ResponseWriter{ResponseWriter: downstream}
	issues := testCriticalErrors()

	err := executeRequest(
		ctx, ctx,
		serverRequest(t, func(request map[string]any) { request["stream"] = true }),
		http.Header{}, "session", provider, writer, issues, nil,
	)
	if err != nil {
		t.Fatalf("terminal response returned error: %v", err)
	}
	select {
	case <-blocked:
		t.Fatal("router read upstream after the terminal response event")
	default:
	}

}

func TestExecuteRequestResponseStartDeadlineLifecycle(t *testing.T) {
	started := make(chan struct{})
	provider := serverProviderFunc(func(startCtx, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		<-startCtx.Done()
		return nil, startCtx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- executeRequest(
			ctx, t.Context(), serverRequest(t, nil), http.Header{}, "session", provider, io.Discard,
			nil, nil,
		)
	}()
	<-started
	err := <-result
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
}

func TestExecuteRequestIndependentUpstreamCancellationIsFailure(t *testing.T) {
	provider := serverProviderFunc(func(context.Context, context.Context, []byte, http.Header, string) (*http.Response, error) {
		return nil, context.Canceled
	})
	issues := testCriticalErrors()
	err := executeRequest(
		t.Context(), t.Context(), serverRequest(t, nil), http.Header{}, "session",
		provider, io.Discard, issues, nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want wrapped upstream cancellation", err)
	}
}

const testProviderBaseURL = "https://provider.example"

type serverRoundTripper func(*http.Request) (*http.Response, error)

func (f serverRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestModelsHandlerForwardsCodexAuthenticationQueryAndResponse(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Unrelated-Caller-Data") != "" {
			t.Error("forwarded unrelated caller data")
		}
		if request.Method != http.MethodGet || request.URL.String() != testProviderBaseURL+"/models?client_version=0.146.0" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		for name, want := range map[string]string{
			"Authorization":      "Bearer caller-token",
			"ChatGPT-Account-ID": "caller-account",
			"Accept":             "application/json",
			"Originator":         "catalog-originator",
			"User-Agent":         "catalog-agent/1.0",
		} {
			if got := request.Header.Get(name); got != want {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		responseHeaders := make(http.Header)
		responseHeaders.Set("Content-Type", "application/json")
		responseHeaders.Set("Cache-Control", "max-age=60")
		responseHeaders.Set("ETag", "catalog-version")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     responseHeaders,
			Body:       io.NopCloser(strings.NewReader(`{"models":[]}`)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.146.0", nil)
	request.Header = codexAuthHeaders()
	request.Header.Set("Originator", "catalog-originator")
	request.Header.Set("User-Agent", "catalog-agent/1.0")
	request.Header.Set("X-Unrelated-Caller-Data", "private")
	recorder := httptest.NewRecorder()

	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, (*requestexecution.Failures)(nil).ReportCatalog)(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"models":[]}` {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	for name, want := range map[string]string{
		"Content-Type":  "application/json",
		"Cache-Control": "max-age=60",
		"ETag":          "catalog-version",
	} {
		if got := recorder.Header().Get(name); got != want {
			t.Errorf("response header %s = %q, want %q", name, got, want)
		}
	}
}

func TestModelsHandlerRejectsUpstreamBodyReadFailure(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Cache-Control": []string{"max-age=60"}},
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader(`{"models":[`),
				iotest.ErrReader(errors.New("upstream read failed")),
			)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, (*requestexecution.Failures)(nil).ReportCatalog)(recorder, request)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "upstream read failed") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `{"models":[`) {
		t.Fatalf("response exposes partial upstream body: %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "" {
		t.Errorf("response Cache-Control = %q, want empty", got)
	}
}

func TestModelsHandlerRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(io.LimitReader(serverRepeatingReader{}, inferencegateway.MaxCatalogBytes+1)),
		}, nil
	})}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, (*requestexecution.Failures)(nil).ReportCatalog)(recorder, request)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "router buffer budget") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestModelsHandlerRejectsMissingAuthentication(t *testing.T) {
	var forwarded bool
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		forwarded = true
		return serverHTTPResponse("{}"), nil
	})}
	recorder := httptest.NewRecorder()

	issues := testCriticalErrors()
	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, issues.ReportCatalog)(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
	)

	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "Authorization") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if forwarded {
		t.Fatal("unauthenticated models request reached upstream")
	}
	if notices := issues.Notices.Pending(); len(notices) != 0 {
		t.Fatalf("catalog failure produced terminal notices: %v", notices)
	}
}

func TestModelsHandlerReportsCompleteForwardFailure(t *testing.T) {
	secret := "Bearer should-not-appear"
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("%s: %w", secret, syscall.ECONNRESET)
	})}
	issues := testCriticalErrors()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	request.Header.Set(codexresponses.RoutingSessionHeader, "models-session")
	debug := diagnosticDebugOutput(t)
	request = request.WithContext(requestexecution.WithDebug(request.Context(), debug))
	recorder := httptest.NewRecorder()

	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, issues.ReportCatalog)(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", recorder.Code)
	}
	if notices := issues.Notices.Pending(); len(notices) != 0 {
		t.Fatalf("catalog failure produced terminal notices: %v", notices)
	}
	if !strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("incomplete HTTP diagnostic: %s", recorder.Body.String())
	}
	raw, err := os.ReadFile(debug.Paths()[0])
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Event               string `json:"event"`
		DiagnosticCode      string `json:"diagnostic_code"`
		DiagnosticReference string `json:"diagnostic_reference"`
		Error               string `json:"error"`
		SessionID           string `json:"session_id"`
	}
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(raw), []byte("\n"))[1], &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != "models_request_failure" || event.DiagnosticCode != "models_upstream_connection_reset" ||
		event.DiagnosticReference == "" || event.SessionID != "models-session" ||
		!strings.Contains(event.Error, secret) {
		t.Fatalf("uncorrelated or incomplete models debug event: %s", raw)
	}
}

func TestModelsHandlerReportsUpstreamStatusAndBody(t *testing.T) {
	secret := "private upstream body"
	httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable,
			Body: io.NopCloser(strings.NewReader(secret))}, nil
	})}
	issues := testCriticalErrors()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	recorder := httptest.NewRecorder()

	inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, httpClient), false, issues.ReportCatalog)(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	if notices := issues.Notices.Pending(); len(notices) != 0 {
		t.Fatalf("catalog failure produced terminal notices: %v", notices)
	}
	if recorder.Body.String() != secret {
		t.Fatalf("changed upstream response: %q", recorder.Body.String())
	}
}

func TestInferenceHTTPRejectionRetainsCompleteError(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"rejected","param":"authorization"}}`,
		"plain upstream rejection",
	} {
		t.Run(body[:min(len(body), 8)], func(t *testing.T) {
			directory := t.TempDir()
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			issues := testCriticalErrors()
			issues.Configure(requestexecution.FailureStorage{Store: store.publication, Limits: store.retentionLimits(), Diagnostics: testDiagnostics(nil, nil)})
			debug := diagnosticDebugOutput(t)
			ctx := requestexecution.WithDebug(t.Context(), debug)
			provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}}}}
			var output bytes.Buffer
			if err := executeRequest(ctx, ctx, serverRequest(t, nil), http.Header{}, "error-session",
				provider, &output, issues, nil); err != nil {
				t.Fatal(err)
			}
			if output.String() != body || len(issues.Notices.Pending()) != 1 || !strings.Contains(issues.Notices.Pending()[0], body) {
				t.Fatal("HTTP rejection body missing from delivery or notice")
			}
			records, err := testDiagnostics(nil, nil).ReadFailures(t.Context(), directory, "")
			if err != nil || len(records) != 1 || !strings.Contains(records[0].Error, body) {
				t.Fatal("HTTP rejection body missing from durable failure record")
			}
			logged, err := os.ReadFile(debug.Paths()[0])
			if err != nil {
				t.Fatal(err)
			}
			var recordedError string
			for line := range strings.SplitSeq(strings.TrimSpace(string(logged)), "\n") {
				var event struct {
					Event string `json:"event"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.Event == "request_complete" {
					recordedError = event.Error
				}
			}
			if !strings.Contains(recordedError, body) {
				t.Fatal("HTTP rejection body missing from debug event")
			}
		})
	}
}

func TestModelsHandlerCatalogFailuresDoNotProduceTerminalNotices(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		failure error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "deadline", failure: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				if test.failure != nil {
					return nil, test.failure
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader("private body"))}, nil
			})}
			issues := testCriticalErrors()
			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			request.Header = codexAuthHeaders()
			inferencegateway.ModelsHandler(newTestProviderClient(testProviderBaseURL, client), false, issues.ReportCatalog)(httptest.NewRecorder(), request)
			if notices := issues.Notices.Pending(); len(notices) != 0 {
				t.Fatalf("catalog failure produced terminal notices: %v", notices)
			}
			issues.Report(requestexecution.Failure{
				Phase: "forward",
			}, errors.New("inference forward failed"))
			if notices := issues.Notices.Pending(); len(notices) != 1 {
				t.Fatalf("inference failure lost its terminal notice: %v", notices)
			}
		})
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientForwardsCodexAuthenticationAndRequestHeaders(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set("Originator", "caller-originator")
	headers.Set("User-Agent", "caller-agent")
	headers.Set("X-Unrelated-Caller-Data", "not-forwarded")

	headers.Add(codexresponses.RoutingSessionHeader, "session-primary")
	headers.Add(codexresponses.RoutingSessionHeader, "session-secondary")
	headers.Set(codexresponses.ThreadIDHeader, "thread")
	headers.Set(codexresponses.ClientRequestIDHeader, "client-request")
	headers.Set(codexresponses.WindowIDHeader, "window:0")
	headers.Set(codexresponses.BetaFeaturesHeader, "feature")
	headers.Set(codexresponses.ResponsesLiteHeader, "true")
	headers.Set(codexresponses.SubagentHeader, threadSpawnSubagent)
	headers.Set(codexresponses.TurnMetadataHeader, "metadata")
	headers.Set("x-codex-turn-state", "opaque-turn-state")
	headers.Set("X-Mekugi-Capture-Id", "must-not-forward")
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		trusted := map[string]string{
			"Authorization":      "Bearer caller-token",
			"ChatGPT-Account-ID": "caller-account",
			"Originator":         "caller-originator",
			"User-Agent":         "caller-agent",
		}
		for name, value := range trusted {
			if got := request.Header.Get(name); got != value {
				t.Errorf("header %s = %q, want %q", name, got, value)
			}
		}
		for _, name := range []string{codexresponses.ThreadIDHeader, codexresponses.ClientRequestIDHeader, codexresponses.WindowIDHeader, codexresponses.BetaFeaturesHeader, codexresponses.ResponsesLiteHeader, codexresponses.SubagentHeader, codexresponses.TurnMetadataHeader, "x-codex-turn-state"} {
			if got, want := request.Header.Values(name), headers.Values(name); !slices.Equal(got, want) {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		if got := request.Header[codexresponses.SessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
			t.Errorf("%s = %q, want cache key", codexresponses.SessionIDHeader, got)
		}
		if got := request.Header.Get(codexresponses.RoutingSessionHeader); got != "" {
			t.Errorf("%s = %q, want empty", codexresponses.RoutingSessionHeader, got)
		}
		if got := request.Header.Values("X-Unrelated-Caller-Data"); len(got) != 0 {
			t.Errorf("unrelated caller header was forwarded: %q", got)
		}
		if got := request.Header.Get("X-Mekugi-Capture-Id"); got != "" {
			t.Error("forwarded private capture identity")
		}
		return serverHTTPResponse("{}"), nil
	})}
	client := newTestProviderClient(testProviderBaseURL, httpClient)
	client.OpenAI.StreamIdleTimeout = time.Minute
	response, err := client.ForwardExecution(t.Context(), t.Context(), []byte("{}"), headers, "cache-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func codexAuthHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer caller-token")
	headers.Set(openai.AccountIDHeader, "caller-account")
	return headers
}

func TestProviderClientRejectsMissingOrMalformedCodexAuthentication(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{"missing authorization", http.Header{openai.AccountIDHeader: []string{"account"}}, "Authorization"},
		{"multiple authorizations", http.Header{"Authorization": []string{"Bearer one", "Bearer two"}, openai.AccountIDHeader: []string{"account"}}, "Authorization"},
		{"unsupported scheme", http.Header{"Authorization": []string{"Basic token"}, openai.AccountIDHeader: []string{"account"}}, "invalid bearer"},
		{"empty token", http.Header{"Authorization": []string{"Bearer "}, openai.AccountIDHeader: []string{"account"}}, "invalid bearer"},
		{"missing account", http.Header{"Authorization": []string{"Bearer token"}}, "ChatGPT-Account-ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var forwarded bool
			httpClient := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
				forwarded = true
				return serverHTTPResponse("{}"), nil
			})}
			client := newTestProviderClient(testProviderBaseURL, httpClient)
			_, err := client.ForwardExecution(t.Context(), t.Context(), []byte("{}"), test.headers, "")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if forwarded {
				t.Fatal("request with invalid authentication reached upstream")
			}
		})
	}
}

func TestProviderClientOmitsUnsafeCodexCacheKey(t *testing.T) {
	for name, cacheKey := range map[string]string{
		"control byte": "cache\nkey",
	} {
		t.Run(name, func(t *testing.T) {
			var forwarded bool
			httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
				forwarded = true
				if values, ok := request.Header[codexresponses.SessionIDHeader]; ok {
					t.Errorf("%s = %q, want omitted", codexresponses.SessionIDHeader, values)
				}
				return serverHTTPResponse("{}"), nil
			})}
			client := newTestProviderClient(testProviderBaseURL, httpClient)
			response, err := client.ForwardExecution(t.Context(), t.Context(), []byte("{}"), codexAuthHeaders(), cacheKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if !forwarded {
				t.Fatal("request did not reach upstream")
			}
		})
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientPreservesCodexRequestHeaders(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(codexresponses.RoutingSessionHeader, "session")
	headers.Set(codexresponses.ThreadIDHeader, "thread")
	headers.Set(codexresponses.ClientRequestIDHeader, "client-request")
	headers.Set(codexresponses.WindowIDHeader, "window:0")
	headers.Set(codexresponses.BetaFeaturesHeader, "feature")
	headers.Set(codexresponses.ResponsesLiteHeader, "true")
	headers.Set(codexresponses.TurnMetadataHeader, "metadata")
	headers.Set("x-codex-turn-state", "opaque-turn-state")
	var attempts []http.Header
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		attempts = append(attempts, request.Header.Clone())
		return serverHTTPResponse("{}"), nil
	})}
	client := newTestProviderClient(testProviderBaseURL, httpClient)
	response, err := client.ForwardExecution(t.Context(), t.Context(), []byte("{}"), headers, "cache-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("upstream attempts = %d, want 1", len(attempts))
	}
	for attempt, forwarded := range attempts {
		if got := forwarded[codexresponses.SessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
			t.Errorf("attempt %d %s = %q, want cache key", attempt+1, codexresponses.SessionIDHeader, got)
		}
		if got := forwarded.Get(codexresponses.RoutingSessionHeader); got != "" {
			t.Errorf("attempt %d %s = %q, want empty", attempt+1, codexresponses.RoutingSessionHeader, got)
		}
		for _, name := range []string{codexresponses.ThreadIDHeader, codexresponses.ClientRequestIDHeader, codexresponses.WindowIDHeader, codexresponses.BetaFeaturesHeader, codexresponses.ResponsesLiteHeader, codexresponses.TurnMetadataHeader, "x-codex-turn-state"} {
			if got, want := forwarded.Values(name), headers.Values(name); !slices.Equal(got, want) {
				t.Errorf("attempt %d header %s = %q, want %q", attempt+1, name, got, want)
			}
		}
	}
}

//nolint:canonicalheader // Exact lowercase names match Codex's observed wire headers.
func TestProviderClientCancelsRequestWithCodexRequestHeaders(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(codexresponses.RoutingSessionHeader, "session")
	started := make(chan http.Header, 1)
	httpClient := &http.Client{Transport: serverRoundTripper(func(request *http.Request) (*http.Response, error) {
		started <- request.Header.Clone()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	client := newTestProviderClient(testProviderBaseURL, httpClient)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := client.ForwardExecution(ctx, ctx, []byte("{}"), headers, "cache-key")
		result <- err
	}()

	var forwarded http.Header
	select {
	case forwarded = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not start")
	}
	if got := forwarded[codexresponses.SessionIDHeader]; !slices.Equal(got, []string{"cache-key"}) {
		t.Fatalf("forwarded %s = %q, want cache key", codexresponses.SessionIDHeader, got)
	}
	if got := forwarded.Get(codexresponses.RoutingSessionHeader); got != "" {
		t.Fatalf("forwarded %s = %q, want empty", codexresponses.RoutingSessionHeader, got)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled request error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not stop after cancellation")
	}
}

func TestCopyJSONTransformedRejectsBodyBeyondRouterBufferBudget(t *testing.T) {
	t.Parallel()
	_, err := codexresponses.CopyBody(io.Discard, io.LimitReader(serverRepeatingReader{}, codexresponses.MaxResponseBytes+1), false, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "router buffer budget") {
		t.Fatalf("error = %v, want router buffer budget rejection", err)
	}
}

func TestExecuteRequestUnsafeCacheKeyRetainsSessionAffinity(t *testing.T) {
	for _, key := range []string{" padded ", "line\nbreak"} {
		parsed := serverRequest(t, func(request map[string]any) {
			request["prompt_cache_key"] = key
			request["access_programs"] = map[string]string{"cyber": "standard"}
		})
		original := bytes.Clone(requestTestBody(t, parsed))
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
		if err := executeRequest(t.Context(), t.Context(), parsed, http.Header{}, "stable-session", provider, io.Discard, nil, nil); err != nil {
			t.Fatal(err)
		}
		if provider.forwardedCacheKey[0] != "stable-session" {
			t.Fatal("unsafe body key disabled valid session affinity")
		}
		if !bytes.Equal(provider.forwarded[0], original) {
			t.Fatal("cache routing fallback rewrote client request")
		}
	}
}

func requestTestBody(t *testing.T, request codexresponses.Request) []byte {
	t.Helper()
	body, err := request.WireBody(request.Fields)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
