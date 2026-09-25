package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func socketWrite(t *testing.T, ctx context.Context, conn *websocket.Conn, value any) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, mustMarshalJSON(value)); err != nil {
		t.Fatal(err)
	}
}

func socketRead(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]json.RawMessage {
	t.Helper()
	_, body, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// Provider handlers propagate errors to their caller instead of invoking FailNow
// outside the test goroutine.
func providerSocketWrite(ctx context.Context, conn *websocket.Conn, value any) error {
	return conn.Write(ctx, websocket.MessageText, mustMarshalJSON(value))
}

func providerSocketRead(ctx context.Context, conn *websocket.Conn) (map[string]json.RawMessage, error) {
	// Protocol fixtures include the full instruction/tool catalog. Use the
	// production budgets rather than the WebSocket library's 32 KiB default.
	conn.SetReadLimit(max(responsesRequestBufferBytes, upstreamJSONBufferBytes))
	_, body, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func socketEvent(kind, id string) map[string]any {
	status := strings.TrimPrefix(kind, "response.")
	return map[string]any{"type": kind, "response": map[string]any{"id": id, "status": status, "output": []any{}}}
}

func testResponsesSocket(t *testing.T, ctx context.Context, upstream http.Handler, proxy *mekugiProxy, headers http.Header) *websocket.Conn {
	t.Helper()
	provider := httptest.NewServer(upstream)
	t.Cleanup(provider.Close)
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(provider.URL, provider.Client()), nil, proxy, nil)
	t.Cleanup(endpoint.Close)
	router := httptest.NewServer(endpoint)
	t.Cleanup(router.Close)
	conn, _, err := websocket.Dial(ctx, router.URL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func TestResponsesWebSocketSteeringAndAutomaticSuccessor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	upstreamDone := make(chan struct{})
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		create, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(create, "type") != "response.create" {
			t.Errorf("create = %s", mustMarshalJSON(create))
		}
		if string(create["access_programs"]) != `{"cyber":"standard"}` {
			t.Errorf("access programs = %s", create["access_programs"])
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "parent")); err != nil {
			t.Error(err)
			return
		}
		steer, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(steer, "type") != "response.steer" || jsonString(steer, "input") != "change direction" {
			t.Errorf("steer = %s", mustMarshalJSON(steer))
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.steer.accepted", "steer": map[string]string{"id": "s1", "previous_response_id": "parent"}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.incomplete", "response": map[string]any{
			"id": "parent", "status": "incomplete", "incomplete_details": map[string]string{"reason": "steered"}, "output": []any{},
		}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "successor")); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "successor")); err != nil {
			t.Error(err)
			return
		}
		// There must be no fabricated response.create for the automatic successor.
		continuation, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(continuation, "previous_response_id") != "successor" {
			t.Errorf("continuation = %s", mustMarshalJSON(continuation))
		}
		var input []map[string]json.RawMessage
		_ = json.Unmarshal(continuation["input"], &input)
		if len(input) != 1 || jsonString(input[0], "content") != "next task" {
			t.Errorf("cached/steering input was replayed: %s", continuation["input"])
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "last")); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = upstream.Read(ctx)
	}), nil, codexAuthHeaders())
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "initial"})
	if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.created" {
		t.Fatalf("created = %s", mustMarshalJSON(got))
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.steer", "previous_response_id": "parent", "input": "change direction"})
	for _, kind := range []string{"response.steer.accepted", "response.incomplete", "response.created", "response.completed"} {
		if got := socketRead(t, ctx, conn); jsonString(got, "type") != kind {
			t.Fatalf("got %s, want %s", mustMarshalJSON(got), kind)
		}
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "previous_response_id": "successor", "input": []any{map[string]string{"role": "user", "content": "next task"}}})
	if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.completed" {
		t.Fatalf("last = %s", mustMarshalJSON(got))
	}
	conn.CloseNow()
	select {
	case <-upstreamDone:
	case <-ctx.Done():
		t.Fatal("downstream disconnect did not close dedicated provider connection")
	}
}

func TestResponsesWebSocketPendingAndPrewarm(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		prewarm, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if string(prewarm["generate"]) != "false" {
			t.Errorf("prewarm = %s", mustMarshalJSON(prewarm))
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "warm")); err != nil {
			t.Error(err)
			return
		}
		create, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(create, "previous_response_id") != "warm" || string(create["input"]) != "[]" || len(create["generate"]) != 0 {
			t.Errorf("prewarm continuation = %s", mustMarshalJSON(create))
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "parent")); err != nil {
			t.Error(err)
			return
		}
		if _, err := providerSocketRead(ctx, upstream); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.steer.accepted", "steer": map[string]string{"id": "s1", "previous_response_id": "parent"}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "parent", "status": "completed", "output": []any{map[string]string{"type": "function_call", "id": "tool", "call_id": "call", "name": "lookup", "arguments": "{}", "status": "completed"}},
		}}); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.steer.pending", "steer": map[string]string{"id": "s1", "previous_response_id": "parent"},
			"reason": "waiting_for_required_input", "required_input": []any{map[string]string{"type": "function_call_output", "call_id": "call", "name": "lookup"}}}); err != nil {
			t.Error(err)
			return
		}
		next, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(next, "previous_response_id") != "parent" {
			t.Errorf("continuation parent = %s", mustMarshalJSON(next))
		}
		var input []map[string]json.RawMessage
		_ = json.Unmarshal(next["input"], &input)
		if len(input) != 1 || jsonString(input[0], "call_id") != "call" || jsonString(input[0], "output") != "result" {
			t.Errorf("pending steering replayed or tool input lost: %s", next["input"])
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "successor")); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "successor")); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = upstream.Read(ctx)
	}), nil, codexAuthHeaders())
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "initial", "generate": false})
	_ = socketRead(t, ctx, conn)
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "previous_response_id": "warm", "input": []any{}})
	_ = socketRead(t, ctx, conn)
	socketWrite(t, ctx, conn, map[string]any{"type": "response.steer", "previous_response_id": "parent", "input": "after tool"})
	for _, kind := range []string{"response.steer.accepted", "response.completed", "response.steer.pending"} {
		if got := socketRead(t, ctx, conn); jsonString(got, "type") != kind {
			t.Fatalf("got %s, want %s", mustMarshalJSON(got), kind)
		}
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "previous_response_id": "parent", "input": []any{map[string]string{"type": "function_call_output", "call_id": "call", "output": "result"}}})
	_ = socketRead(t, ctx, conn)
	if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.completed" {
		t.Fatalf("completion = %s", mustMarshalJSON(got))
	}
}

func TestResponsesWebSocketSteeringAdmissionHistory(t *testing.T) {
	input := func(text string) []json.RawMessage {
		items, err := webSocketInput(mustMarshalJSON(text))
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	s := &responsesWebSocket{steers: []webSocketSteer{
		{id: "rejected", parent: "parent", input: input("must not be retained")},
		{parent: "parent", input: input("accepted after continuation send")},
		{id: "other", parent: "other-parent", input: input("different parent")},
	}}
	history := &webSocketHistory{input: input("tool continuation")}
	s.observeControl([]byte(`{"type":"response.steer.failed","steer":{"id":"rejected","previous_response_id":"parent"}}`))
	s.observeControl([]byte(`{"type":"response.steer.accepted","steer":{"id":"late","previous_response_id":"parent"}}`))
	s.commitSteering(history, "parent")
	got := string(mustMarshalJSON(history.input))
	if strings.Contains(got, "must not be retained") || strings.Contains(got, "different parent") ||
		!strings.Contains(got, "accepted after continuation send") || len(history.input) != 2 {
		t.Fatalf("admitted history = %s", got)
	}
	if len(s.steers) != 1 || s.steers[0].id != "other" {
		t.Fatalf("wrong steering submissions consumed: %#v", s.steers)
	}
	again := &webSocketHistory{}
	s.commitSteering(again, "parent")
	if len(again.input) != 0 {
		t.Fatal("accepted steering replayed twice")
	}
}

func TestResponsesWebSocketGrokPrewarmContinuationAndDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := make(chan []byte, 1)
	stopped := make(chan struct{})
	provider := newProviderClient("http://unused.invalid", nil)
	provider.grok = &grokClient{auth: newGrokAuth("", "test"), httpClient: &http.Client{
		Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			started <- body
			<-request.Context().Done()
			close(stopped)
			return nil, request.Context().Err()
		}),
	}}
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, provider, nil, nil, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: grokTestHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": grokModel, "generate": false, "input": "warm up"})
	event := socketRead(t, ctx, conn)
	if jsonString(event, "type") != "response.completed" {
		t.Fatalf("prewarm response = %s", mustMarshalJSON(event))
	}
	var response map[string]json.RawMessage
	_ = json.Unmarshal(event["response"], &response)
	select {
	case <-started:
		t.Fatal("Grok prewarm generated inference")
	default:
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": grokModel, "previous_response_id": jsonString(response, "id"), "input": []any{}})
	select {
	case body := <-started:
		if !strings.Contains(string(body), "warm up") || strings.Contains(string(body), "previous_response_id") {
			t.Fatalf("Grok continuation lost native history: %s", body)
		}
	case <-ctx.Done():
		t.Fatal("Grok continuation did not start")
	}
	conn.CloseNow()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("downstream disconnect did not cancel blocked Grok execution")
	}
}

func TestResponsesWebSocketEndpointCloseWaitsAndRejectsNewAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	upstreamClosed := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		defer close(upstreamClosed)
		if _, err := providerSocketRead(ctx, conn); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, conn, socketEvent("response.created", "active")); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.Read(ctx)
	}))
	defer provider.Close()
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, newProviderClient(provider.URL, provider.Client()), nil, nil, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"})
	_ = socketRead(t, ctx, conn)
	closed := make(chan struct{})
	go func() { endpoint.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("endpoint shutdown did not await handler cancellation")
	}
	select {
	case <-upstreamClosed:
	case <-ctx.Done():
		t.Fatal("endpoint shutdown left provider connection open")
	}
	rejected, response, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: codexAuthHeaders()})
	if rejected != nil {
		rejected.CloseNow()
	}
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("admission after shutdown: response=%v err=%v", response, err)
	}
}

func TestResponsesWebSocketStartupPrewarmMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	proxy := newManagedMekugiProxy(t)

	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "prewarm-session")
	// Startup metadata need not contain a thread identity yet.
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		for i, id := range []string{"warm", "turn", "continuation"} {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if i == 0 {
				if string(request["generate"]) != "false" {
					t.Errorf("warmup may generate: %s", mustMarshalJSON(request))
				}
			} else {
				if len(request["generate"]) != 0 {
					t.Error("warmup generate setting inherited")
				}
				if !strings.Contains(string(request["tools"]), "apply_patch") ||
					!strings.Contains(string(request["tools"]), "exec_command") ||
					strings.Contains(string(request["tools"]), `"shell"`) {
					t.Errorf("ordinary turn tools were not preserved: %s", request["tools"])
				}
				wantParent := "warm"
				wantInput := "task"
				if i == 2 {
					wantParent = "turn"
					wantInput = "next"
				}
				if jsonString(request, "previous_response_id") != wantParent {
					t.Errorf("parent = %s", request["previous_response_id"])
				}
				var input []map[string]json.RawMessage
				if err := json.Unmarshal(request["input"], &input); err != nil || len(input) != 1 || jsonString(input[0], "content") != wantInput {
					t.Errorf("incremental history = %s", request["input"])
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	metadata := func(value any) map[string]string {
		return map[string]string{codexTurnMetadataHeader: string(mustMarshalJSON(value)), threadIDHeader: "prewarm-thread"}
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{map[string]string{"role": "user", "content": "warmup context"}}, "generate": false, "client_metadata": map[string]string{codexTurnMetadataHeader: `{"request_kind":"prewarm"}`}})
	if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.completed" {
		t.Fatalf("warmup produced a warning: %s", mustMarshalJSON(got))
	}
	turnMetadata := metadata(codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{t.TempDir(): nil}})
	for i, parent := range []string{"warm", "turn"} {
		input := "task"
		if i == 1 {
			input = "next"
		}
		socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{map[string]string{"role": "user", "content": input}}, "previous_response_id": parent, "tools": testNativeResponsesTools(), "client_metadata": turnMetadata})
		if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.completed" {
			t.Fatalf("turn produced a warning: %s", mustMarshalJSON(got))
		}
	}
}

func TestMekugiPrewarmRequiresExplicitNonGeneratingRequest(t *testing.T) {
	for _, generate := range []string{"", "true", "null", `"false"`} {
		t.Run("generate="+generate, func(t *testing.T) {
			request, err := parseResponsesRequest([]byte(`{"model":"gpt-test","input":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			if generate != "" {
				request.fields["generate"] = json.RawMessage(generate)
			}
			proxy := &mekugiProxy{}
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "prewarm", nil), "session", &webSocketExchange{}, io.Discard, nil, proxy, nil); err == nil {
				t.Fatal("generating prewarm bypassed turn validation")
			}
		})
	}
}

func TestResponsesHTTPPrewarmCannotBypassPreparation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"grok-test","input":[],"generate":false}`))
	request.Header = serverMetadataHeaders(t, "prewarm", nil)
	request.Header.Set(sessionIDHeader, "prewarm-session")
	request.Header.Set(threadIDHeader, "prewarm-thread")
	provider := &serverFakeProvider{}
	recorder := httptest.NewRecorder()
	responsesHandler(t.Context(), time.Minute, provider, nil, &mekugiProxy{}, nil)(recorder, request)
	if len(provider.forwarded) != 0 {
		t.Fatal("HTTP prewarm reached a potentially generating provider")
	}
	if !strings.Contains(recorder.Body.String(), "valid turn metadata") {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestResponsesWebSocketLocalErrorStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		metadata string
		status   string
	}{
		{name: "invalid JSON", body: `{`, status: "400"},
		{name: "invalid input", body: `{"type":"response.create","model":"gpt-test","input":42}`, status: "400"},
		{name: "unknown parent", body: `{"type":"response.create","model":"gpt-test","previous_response_id":"missing"}`, status: "400"},
		{name: "execution failure", body: `{"type":"response.create","model":"gpt-test","input":[]}`, metadata: "invalid", status: "502"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			headers := codexAuthHeaders()
			headers.Set(sessionIDHeader, "error-session")
			headers.Set(threadIDHeader, "error-thread")
			var proxy *mekugiProxy
			if test.metadata != "" {
				proxy = newManagedMekugiProxy(t)
				metadata := serverMetadataHeaders(t, test.metadata, map[string]json.RawMessage{t.TempDir(): nil})
				headers.Set(codexTurnMetadataHeader, metadata.Get(codexTurnMetadataHeader))
			}
			conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("invalid request reached provider")
			}), proxy, headers)
			if err := conn.Write(ctx, websocket.MessageText, []byte(test.body)); err != nil {
				t.Fatal(err)
			}
			for {
				event := socketRead(t, ctx, conn)
				if jsonString(event, "type") != "error" {
					continue
				}
				if string(event["status"]) != test.status {
					t.Fatalf("status = %s, want %s: %s", event["status"], test.status, mustMarshalJSON(event))
				}
				var detail map[string]json.RawMessage
				if err := json.Unmarshal(event["error"], &detail); err != nil || jsonString(detail, "message") == "" {
					t.Fatalf("missing Codex error detail: %s", event["error"])
				}
				break
			}
		})
	}
}

func TestResponsesWebSocketAutomaticParentErrorStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		if _, err := providerSocketRead(ctx, upstream); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", "parent")); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.created", "response": map[string]string{"id": "automatic", "previous_response_id": "missing"}}); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = upstream.Read(ctx)
	}), nil, codexAuthHeaders())
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "task"})
	if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.completed" {
		t.Fatalf("completion = %s", mustMarshalJSON(event))
	}
	event := socketRead(t, ctx, conn)
	if jsonString(event, "type") != "error" || string(event["status"]) != "502" {
		t.Fatalf("provider ancestry must not be a client error: %s", mustMarshalJSON(event))
	}
}
