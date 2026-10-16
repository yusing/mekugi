package router

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/libraries/httptransport"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestStreamDiagnosticsReadTermination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		want      string
		closeCode int
	}{
		{"eof", io.EOF, "upstream_eof", 0},
		{"unexpected", io.ErrUnexpectedEOF, "upstream_unexpected_eof", 0},
		{"cancel", context.Canceled, "upstream_canceled", 0},
		{"deadline", context.DeadlineExceeded, "upstream_deadline", 0},
		{"idle", errors.Join(httptransport.ErrStreamIdleTimeout, context.DeadlineExceeded), "upstream_idle_timeout", 0},
		{"close", fmt.Errorf("secret wrapper: %w", websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "secret reason"}), "upstream_websocket_close_1001", 1001},
		{"unknown", errors.New("secret transport"), "upstream_transport_unknown", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &transportcapture.Stream{}
			_, _ = copySSETransformed(io.Discard, diagnosticErrorReader{tc.err}, nil, &responseHooks{streamDiagnostics: d})
			if d.ReadTermination != tc.want || d.WebSocketCloseCode != tc.closeCode {
				t.Fatalf("classification: %+v", d)
			}
			if strings.Contains(string(responses.MustMarshalJSON(d.Snapshot())), "secret") {
				t.Fatal("error contents leaked")
			}
		})
	}
}

type diagnosticErrorReader struct{ err error }

func (r diagnosticErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestStreamDiagnosticsRequestLog(t *testing.T) {
	for _, stream := range []string{"", "data: {\"type\":\"response.in_progress\"}\n\n"} {
		t.Run(fmt.Sprintf("bytes_%d", len(stream)), func(t *testing.T) {
			debug := diagnosticDebugOutput(t)
			ctx := context.WithValue(t.Context(), debugContextKey{}, debug)
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
			provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}}}}
			recorder, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "passthrough"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = recorder.Close() })
			recorder.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_ = executeRequest(r.Context(), r.Context(), request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, io.Discard, NewCriticalErrors(), nil)
			})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx))
			data, err := os.ReadFile(debug.Paths()[0])
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
				var event map[string]json.RawMessage
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if responses.StringField(event, "event") != "request_complete" {
					continue
				}
				found = true
				var diagnostic transportcapture.Stream
				if err := json.Unmarshal(event["response_stream"], &diagnostic); err != nil {
					t.Fatal(err)
				}
				if diagnostic.ReadTermination != "upstream_eof" || diagnostic.CopyStop != "eof_without_terminal" {
					t.Fatalf("missing stream evidence: %s", line)
				}
			}
			if !found {
				t.Fatal("missing request_complete")
			}
		})
	}
}

func TestStreamDiagnosticsWebSocketClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		for _, event := range []string{
			`{"type":"codex.response.metadata","headers":{"x-request-id":"req_ws"}}`,
			`{"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"item_ws","call_id":"call_ws"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"item_ws","delta":"private patch"}`,
		} {
			if err := conn.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusGoingAway, "private close reason")
	}))
	defer upstream.Close()
	client := newProviderClient(upstream.URL, upstream.Client())
	client.openai.EnableWebSockets(ctx)
	defer client.openai.Close()
	response, err := client.forwardExecution(ctx, ctx, []byte(`{"model":"gpt-test","stream":true,"input":[]}`), http.Header{"Authorization": {"Bearer test"}, openai.AccountIDHeader: {"account"}}, "session")
	if err != nil {
		t.Fatal(err)
	}
	d := &transportcapture.Stream{}
	_, err = copyUpstreamBodyTransformed(io.Discard, response, true, nil, &responseHooks{streamDiagnostics: d})
	if err == nil || d.WebSocketCloseCode != 1001 || d.ProviderRequestID != "req_ws" || len(d.Snapshot().PendingCalls) != 1 || d.Snapshot().PendingCalls[0].InputBytes != 13 {
		t.Fatalf("missing websocket evidence: %+v, %v", d, err)
	}
	if strings.Contains(string(responses.MustMarshalJSON(d.Snapshot())), "private") {
		t.Fatal("payload leaked")
	}
}

func TestStreamDiagnosticsNativeDownstreamClose(t *testing.T) {
	messages := make(chan webSocketMessage, 1)
	closeErr := websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private downstream"}
	messages <- webSocketMessage{err: closeErr}
	d := &transportcapture.Stream{}
	exchange := &webSocketExchange{
		ctx: t.Context(), streamDiagnostics: d,
		session: &responsesWebSocket{provider: newProviderClient("http://unused.invalid", nil), clientMessages: messages},
	}
	_, err := copySSETransformed(io.Discard, exchange, nil, &responseHooks{streamDiagnostics: d})
	if !errors.Is(err, closeErr) || d.ReadOrigin != "downstream" || d.ReadTermination != "downstream_websocket_close_1001" || d.WebSocketCloseCode != 1001 {
		t.Fatalf("downstream misattributed: %+v, %v", d, err)
	}
}

func TestStreamDiagnosticsNativeMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	debug := diagnosticDebugOutput(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, err := providerSocketRead(ctx, conn); err != nil {
			return
		}
		if err := providerSocketWrite(ctx, conn, map[string]any{"type": "codex.response.metadata", "headers": map[string]string{"x-request-id": "req_native"}}); err != nil {
			return
		}
		for _, kind := range []string{"response.created", "response.completed"} {
			if err := providerSocketWrite(ctx, conn, socketEvent(kind, "native")); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer upstream.Close()
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(upstream.URL, upstream.Client()), nil, nil)
	defer endpoint.Close()
	recorder, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "passthrough"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	router := httptest.NewServer(recorder.Handler(debug.handler(endpoint)))
	defer router.Close()
	conn, _, err := websocket.Dial(ctx, router.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"})
	for _, want := range []string{"codex.response.metadata", "response.created", "response.completed"} {
		if got := socketRead(t, ctx, conn); responses.StringField(got, "type") != want {
			t.Fatalf("got %s, want %s", responses.MustMarshalJSON(got), want)
		}
	}
	conn.CloseNow()
	endpoint.Close()
	data, err := os.ReadFile(debug.Paths()[0])
	if err != nil {
		t.Fatal(err)
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil || responses.StringField(event, "event") != "request_complete" {
			continue
		}
		var diagnostic transportcapture.Stream
		if err := json.Unmarshal(event["response_stream"], &diagnostic); err != nil {
			t.Fatal(err)
		}
		if diagnostic.ProviderRequestID != "req_native" || diagnostic.TerminalEvent != "response.completed" {
			t.Fatalf("native metadata lost: %s", line)
		}
		return
	}
	t.Fatal("missing native request_complete")
}

func TestStreamDiagnosticsHTTPBodyEnd(t *testing.T) {
	const stream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_smoke\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"private response text\"}\n\n"
	for _, mode := range []string{"fixed", "chunked", "truncated", "http2"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", "req_smoke")
				w.Header().Set("Authorization", "private credential")
				switch mode {
				case "fixed":
					w.Header().Set("Content-Length", strconv.Itoa(len(stream)))
				case "truncated":
					w.Header().Set("Content-Length", strconv.Itoa(len(stream)+20))
				case "chunked", "http2":
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, stream)
			}))
			if mode == "http2" {
				upstream.EnableHTTP2 = true
				upstream.StartTLS()
			} else {
				upstream.Start()
			}
			defer upstream.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := upstream.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			d := &transportcapture.Stream{}
			state, err := copyUpstreamBodyTransformed(io.Discard, response, true, nil, &responseHooks{streamDiagnostics: d})
			if state != responses.TerminalPending {
				t.Fatalf("incomplete response accepted: %v", state)
			}
			if mode == "truncated" {
				if !errors.Is(err, io.ErrUnexpectedEOF) || d.EndReason != "upstream_unexpected_eof" {
					t.Fatalf("truncation lost: %+v, %v", d, err)
				}
			} else if err != nil || d.EndReason != "http_body_complete_without_terminal" {
				t.Fatalf("normal body end misclassified: %+v, %v", d, err)
			}
			wantTransport, wantFraming := "http1", "content_length"
			if mode == "chunked" {
				wantFraming = "chunked"
			} else if mode == "http2" {
				wantTransport, wantFraming = "http2", "stream_end"
			}
			if d.Transport != wantTransport || d.HTTPFraming != wantFraming ||
				d.BodyBytes != uint64(len(stream)) || d.Events != 2 ||
				d.ProviderResponseID != "resp_smoke" || d.ProviderRequestID != "req_smoke" ||
				d.LastByteAt.IsZero() || d.ReadEndedAt.Before(d.LastByteAt) {
				t.Fatalf("missing end evidence: %+v", d)
			}
			if strings.Contains(string(responses.MustMarshalJSON(d.Snapshot())), "private") {
				t.Fatal("diagnostic leaked response text or arbitrary headers")
			}
		})
	}
}

func TestStreamDiagnosticsPreserveEndOrigin(t *testing.T) {
	d := &transportcapture.Stream{}
	recordStreamReadEnd(d, io.EOF, "downstream")
	recordStreamReadEnd(d, context.Canceled, "context")
	recordStreamReadEnd(d, io.EOF, "")
	recordStreamCopyStop(d, nil)
	if d.ReadOrigin != "downstream" || d.ReadTermination != "downstream_eof" || d.EndReason != "downstream_eof" {
		t.Fatalf("first observed cause was overwritten: %+v", d)
	}
}

func TestStreamDiagnosticsUnterminatedEventAndDecodedBody(t *testing.T) {
	const stream = "data: {\"type\":\"response.in_progress\"}"
	d := &transportcapture.Stream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		_, _ = io.WriteString(compressed, stream)
		_ = compressed.Close()
	}))
	defer upstream.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := upstream.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = copyUpstreamBodyTransformed(io.Discard, response, true, nil, &responseHooks{streamDiagnostics: d})
	if err != nil || !d.UnterminatedEvent || d.EndReason != "http_decoded_body_ended_without_terminal" {
		t.Fatalf("decoded EOF was presented as validated HTTP framing: %+v, %v", d, err)
	}
}

func TestStreamDiagnosticsNativeReaderShutdown(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, origin := range []string{"upstream", "downstream"} {
			t.Run(fmt.Sprintf("%s/canceled_%t", origin, canceled), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				messages := make(chan webSocketMessage)
				close(messages)
				if canceled {
					cancel()
				}
				d := &transportcapture.Stream{}
				session := &responsesWebSocket{ctx: ctx, provider: newProviderClient("http://unused.invalid", nil)}
				if origin == "upstream" {
					session.providerMessages = messages
				} else {
					session.clientMessages = messages
				}
				exchange := &webSocketExchange{ctx: ctx, session: session, streamDiagnostics: d}
				_, err := copyUpstreamBodyTransformed(io.Discard, &http.Response{
					Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: exchange,
				}, true, nil, &responseHooks{streamDiagnostics: d})
				if canceled {
					if !errors.Is(err, context.Canceled) || d.ReadTermination == "upstream_eof" {
						t.Fatalf("reader cancellation became EOF: %+v, %v", d, err)
					}
				} else {
					diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
					if !ok || diagnostic.code != "websocket_reader_closed" || d.ReadOrigin != origin {
						t.Fatalf("reader closure lost its origin: %+v, %v", d, err)
					}
				}
				if d.Transport != "websocket" || d.CopyStop != "read_error" {
					t.Fatalf("synthetic HTTP bridge hid WebSocket failure: %+v", d)
				}
			})
		}
	}
}

func TestNativeReaderPreservesCloseErrorWhenDisconnectCancels(t *testing.T) {
	for range 10 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			_ = conn.Close(websocket.StatusGoingAway, "private peer reason")
		}))
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
		if err != nil {
			cancel()
			upstream.Close()
			t.Fatal(err)
		}
		disconnected := make(chan struct{})
		messages := readResponsesWebSocket(ctx, conn, func() {
			cancel()
			close(disconnected)
		})
		select {
		case <-disconnected:
		case <-ctx.Done():
		}
		message, open := <-messages
		conn.CloseNow()
		upstream.Close()
		if !open || websocket.CloseStatus(message.err) != websocket.StatusGoingAway {
			t.Fatalf("actual close was replaced by a closed channel: open=%v err=%v", open, message.err)
		}
		cancel()
	}
}
