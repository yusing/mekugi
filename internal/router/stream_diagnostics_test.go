package router

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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestStreamDiagnosticsIncompleteHPATCH(t *testing.T) {
	calls := 0
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, &calls))
	d := &streamDiagnostics{}
	hooks := &responseHooks{streamDiagnostics: d}
	input := "secret patch é"
	stream := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"hpatch","status":"in_progress","input":""}}` + "\n\n" +
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","delta":` + string(mustMarshalJSON(input)) + "}\n\n"
	var output bytes.Buffer
	_, err := copyUpstreamBodyTransformed(&output, &http.Response{
		Header: http.Header{"X-Request-Id": {"req_123"}},
		Body:   io.NopCloser(strings.NewReader(stream)),
	}, true, transform, hooks)
	diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
	if !ok || diagnostic.code != "stream_ended_incomplete_mekugi_call" {
		t.Fatalf("expected existing incomplete guard, got %v", err)
	}
	if calls != 0 || strings.Contains(output.String(), input) || strings.Contains(output.String(), "custom_tool_call") {
		t.Fatal("incomplete input was translated or exposed")
	}
	call := d.pending["ctc_1"]
	if call == nil || call.CallID != "call_1" || call.Fragments != 1 || call.InputBytes != uint64(len(input)) || call.InputDone {
		t.Fatalf("missing fragment evidence: %+v", call)
	}
	if d.ReadTermination != "upstream_eof" || d.CopyStop != "translation_error" || d.LastEvent != "response.custom_tool_call_input.delta" || d.LastEventAt.IsZero() || d.ProviderRequestID != "req_123" {
		t.Fatalf("missing termination evidence: %+v", d)
	}
	encoded, err := json.Marshal(d.snapshot())
	if err != nil || bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("unsafe diagnostics: %s, %v", encoded, err)
	}
}

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
		{"idle", errors.Join(errUpstreamStreamIdleTimeout, context.DeadlineExceeded), "upstream_idle_timeout", 0},
		{"close", fmt.Errorf("secret wrapper: %w", websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "secret reason"}), "upstream_websocket_close_1001", 1001},
		{"unknown", errors.New("secret transport"), "upstream_transport_unknown", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &streamDiagnostics{}
			_, _ = copySSETransformed(io.Discard, diagnosticErrorReader{tc.err}, nil, &responseHooks{streamDiagnostics: d})
			if d.ReadTermination != tc.want || d.WebSocketCloseCode != tc.closeCode {
				t.Fatalf("classification: %+v", d)
			}
			if strings.Contains(string(mustMarshalJSON(d.snapshot())), "secret") {
				t.Fatal("error contents leaked")
			}
		})
	}
}

type diagnosticErrorReader struct{ err error }

func (r diagnosticErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestStreamDiagnosticsBoundsAndCompletion(t *testing.T) {
	d := &streamDiagnostics{}
	for i := range maxStreamDiagnosticCalls + 1 {
		d.observe([]byte(fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"function_call","id":"item_%02d","call_id":"call_%02d"}}`, i, i)))
	}
	if len(d.pending) != maxStreamDiagnosticCalls || !d.Truncated {
		t.Fatal("pending diagnostics are not bounded")
	}
	d.observe([]byte(`{"type":"response.function_call_arguments.delta","item_id":"item_00","delta":"secret"}`))
	d.observe([]byte(`{"type":"response.function_call_arguments.done","item_id":"item_00","arguments":"secret"}`))
	if !d.pending["item_00"].InputDone {
		t.Fatal("input completion missing")
	}
	d.observe([]byte(`{"type":"response.output_item.done","item":{"id":"item_00"}}`))
	if d.pending["item_00"] != nil {
		t.Fatal("completed call retained")
	}
	d.observe([]byte(`{"type":"secret event"}`))
	if d.LastEvent != "other" {
		t.Fatal("unknown event leaked")
	}
	d.observe([]byte(`{"type":"codex.response.metadata","headers":{"x-request-id":"req_456","authorization":"secret"}}`))
	d.observe([]byte(`{"type":"response.completed","response":{"status":"completed"}}`))
	d.copyStopped(nil)
	if d.TerminalEvent != "response.completed" || d.CopyStop != "terminal_event" || d.ProviderRequestID != "req_456" {
		t.Fatalf("terminal metadata: %+v", d)
	}
	if strings.Contains(string(mustMarshalJSON(d.snapshot())), "secret") {
		t.Fatal("content leaked")
	}
	other := &streamDiagnostics{}
	if len(other.pending) != 0 || other.LastEvent != "" {
		t.Fatal("request state shared")
	}
	var disabled *streamDiagnostics
	disabled.observe([]byte(`{"type":"error"}`))
	disabled.readEnded(io.EOF)
	disabled.copyStopped(nil)
}

func TestStreamDiagnosticsRequestLog(t *testing.T) {
	for _, stream := range []string{"", "data: {\"type\":\"response.in_progress\"}\n\n"} {
		t.Run(fmt.Sprintf("bytes_%d", len(stream)), func(t *testing.T) {
			debug := featureDebugOutput(t)
			ctx := context.WithValue(t.Context(), debugContextKey{}, debug)
			request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
			provider := &serverFakeProvider{results: []serverForwardResult{{response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}}}}
			_ = executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "diagnostic-session", provider, io.Discard, NewCriticalErrors(), nil, nil, nil)
			data, err := os.ReadFile(debug.paths[0])
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
				var event map[string]json.RawMessage
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if jsonString(event, "event") != "request_complete" {
					continue
				}
				found = true
				var diagnostic streamDiagnostics
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
	client.enableWebSockets(ctx)
	defer client.websockets.close()
	response, err := client.forwardExecution(ctx, ctx, []byte(`{"model":"gpt-test","stream":true,"input":[]}`), http.Header{"Authorization": {"Bearer test"}, chatGPTAccountIDHeader: {"account"}}, "session")
	if err != nil {
		t.Fatal(err)
	}
	d := &streamDiagnostics{}
	_, err = copyUpstreamBodyTransformed(io.Discard, response, true, nil, &responseHooks{streamDiagnostics: d})
	if err == nil || d.WebSocketCloseCode != 1001 || d.ProviderRequestID != "req_ws" || d.pending["item_ws"] == nil || d.pending["item_ws"].InputBytes != 13 {
		t.Fatalf("missing websocket evidence: %+v, %v", d, err)
	}
	if strings.Contains(string(mustMarshalJSON(d.snapshot())), "private") {
		t.Fatal("payload leaked")
	}
}

func TestStreamDiagnosticsNativeDownstreamClose(t *testing.T) {
	messages := make(chan webSocketMessage, 1)
	closeErr := websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private downstream"}
	messages <- webSocketMessage{err: closeErr}
	d := &streamDiagnostics{}
	exchange := &webSocketExchange{
		ctx: t.Context(), streamDiagnostics: d,
		session: &responsesWebSocket{provider: &providerClient{}, clientMessages: messages},
	}
	_, err := copySSETransformed(io.Discard, exchange, nil, &responseHooks{streamDiagnostics: d})
	if !errors.Is(err, closeErr) || d.ReadOrigin != "downstream" || d.ReadTermination != "downstream_websocket_close_1001" || d.WebSocketCloseCode != 1001 {
		t.Fatalf("downstream misattributed: %+v, %v", d, err)
	}
}

func TestStreamDiagnosticsNativeMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	debug := featureDebugOutput(t)
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
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(upstream.URL, upstream.Client()), nil, nil, nil, nil)
	defer endpoint.Close()
	router := httptest.NewServer(debug.handler(endpoint))
	defer router.Close()
	conn, _, err := websocket.Dial(ctx, router.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"})
	for _, want := range []string{"codex.response.metadata", "response.created", "response.completed"} {
		if got := socketRead(t, ctx, conn); jsonString(got, "type") != want {
			t.Fatalf("got %s, want %s", mustMarshalJSON(got), want)
		}
	}
	conn.CloseNow()
	endpoint.Close()
	data, err := os.ReadFile(debug.paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil || jsonString(event, "event") != "request_complete" {
			continue
		}
		var diagnostic streamDiagnostics
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
