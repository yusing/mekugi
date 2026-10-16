package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/libraries/httptransport"

	"github.com/coder/websocket"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// A downstream socket owns a dedicated provider socket. In particular it never
// enters the HTTP pool: accepted steering and previous_response_id are scoped
// to this connection, including the quiet interval after response.completed.
func responsesWebSocketHandler(lifecycle context.Context, timeout time.Duration, provider *providerClient, issues *CriticalErrors, proxy *mekugiProxy) *responsesWebSocketEndpoint {
	lifecycle, cancel := context.WithCancel(lifecycle)
	endpoint := &responsesWebSocketEndpoint{cancel: cancel}
	endpoint.handler = func(w http.ResponseWriter, r *http.Request) {
		if provider.grok == nil && len(provider.opencode) == 0 {
			if _, _, err := openai.Credentials(r.Header); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(codexresponses.MaxRequestBytes)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(lifecycle, cancel)
		defer stop()
		s := &responsesWebSocket{
			ctx: ctx, downstream: conn, provider: provider, headers: r.Header.Clone(),
			timeout: timeout, issues: issues, proxy: proxy,
			clientMessages: readResponsesWebSocket(ctx, conn, cancel),
		}
		defer func() {
			if s.upstream != nil {
				s.upstream.CloseNow()
			}
		}()
		if err := s.run(); err != nil && ctx.Err() == nil && !s.errorDelivered {
			_, _ = s.writeError(ctx, err)
		}
	}
	return endpoint
}

// net/http stops tracking upgraded connections. The endpoint therefore owns
// admission and waits for their handlers before shared router resources close.
type responsesWebSocketEndpoint struct {
	handler http.HandlerFunc
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	active  sync.WaitGroup
}

func (e *responsesWebSocketEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		http.Error(w, "Responses WebSocket endpoint is shutting down", http.StatusServiceUnavailable)
		return
	}
	e.active.Add(1)
	e.mu.Unlock()
	defer e.active.Done()
	e.handler(w, r)
}

func (e *responsesWebSocketEndpoint) Close() {
	e.mu.Lock()
	e.closed = true
	e.cancel()
	e.mu.Unlock()
	e.active.Wait()
}

type webSocketStatusError struct {
	status int
	body   []byte
}

func (e *webSocketStatusError) Error() string {
	if len(e.body) == 0 {
		return fmt.Sprintf("provider WebSocket upgrade returned HTTP %d", e.status)
	}
	return fmt.Sprintf("provider WebSocket upgrade returned HTTP %d: %s", e.status, e.body)
}

func (s *responsesWebSocket) writeError(ctx context.Context, err error) ([]byte, error) {
	status := http.StatusBadGateway
	if _, ok := errors.AsType[*requestCompatibilityError](err); ok {
		status = http.StatusBadRequest
	}
	if rejection, ok := errors.AsType[*providerHTTPError](err); ok {
		status = rejection.status
	}
	fields := map[string]any{"type": "error", "status": status, "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}}
	if upstream, ok := errors.AsType[*webSocketStatusError](err); ok {
		fields["status"] = upstream.status
		var body map[string]json.RawMessage
		if json.Unmarshal(upstream.body, &body) == nil && len(body["error"]) != 0 {
			fields["error"] = body["error"]
		}
	}
	body := responseevents.MustMarshalJSON(fields)
	writeErr := s.downstream.Write(ctx, websocket.MessageText, body)
	s.errorDelivered = writeErr == nil
	return body, writeErr
}

type webSocketMessage struct {
	body []byte
	err  error
}

// A closed reader channel is not evidence of an upstream EOF. Cancellation can
// win the reader's final send; preserve it rather than inventing a transport end.
func webSocketMessageReadError(ctx context.Context, message webSocketMessage) error {
	if message.err != nil {
		return message.err
	}
	if len(message.body) != 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return staticCriticalDiagnostic("websocket_reader_closed", "a WebSocket message reader stopped without delivering a terminal cause")
}

// A peer disconnect can reach the writer before the reader cancels the
// session. Classify it by its transport cause, not goroutine scheduling.
func downstreamDisconnectError(err error) error {
	code := websocket.CloseStatus(err)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		code == websocket.StatusNormalClosure || code == websocket.StatusGoingAway ||
		code == websocket.StatusAbnormalClosure {
		return errors.Join(codexresponses.ErrDownstreamDisconnected, err)
	}
	return err
}

func readResponsesWebSocket(ctx context.Context, conn *websocket.Conn, disconnected context.CancelFunc) <-chan webSocketMessage {
	messages := make(chan webSocketMessage, 1)
	go func() {
		defer close(messages)
		for {
			kind, body, err := conn.Read(ctx)
			if err != nil && disconnected != nil {
				err = downstreamDisconnectError(err)
				disconnected()
			}
			readFailed := err != nil
			if err == nil && kind != websocket.MessageText {
				err = errors.New("responses WebSocket requires text JSON messages")
			}
			if err == nil && !json.Valid(body) {
				err = errors.New("responses WebSocket received invalid JSON")
			}
			if !readFailed && err != nil && disconnected != nil {
				err = incompatibleRequest("invalid_websocket_request", err.Error())
			}
			// disconnected can cancel ctx before this error is published.
			// Preserve the actual close/error whenever the bounded slot is free.
			if err != nil {
				select {
				case messages <- webSocketMessage{body, err}:
					return
				default:
				}
			}
			select {
			case messages <- webSocketMessage{body, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return messages
}

type responsesWebSocket struct {
	history          codexresponses.SessionHistory
	ctx              context.Context
	downstream       *websocket.Conn
	upstream         *websocket.Conn
	provider         *providerClient
	headers          http.Header
	clientMessages   <-chan webSocketMessage
	providerMessages <-chan webSocketMessage
	timeout          time.Duration
	issues           *CriticalErrors
	proxy            *mekugiProxy
	queuedCreate     []byte
	errorDelivered   bool
}

func (s *responsesWebSocket) run() error {
	for {
		if len(s.queuedCreate) != 0 {
			command := s.queuedCreate
			s.queuedCreate = nil
			if err := s.execute(command, nil); err != nil {
				return err
			}
			continue
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case message := <-s.clientMessages:
			if err := webSocketMessageReadError(s.ctx, message); err != nil {
				return err
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if responseevents.StringField(fields, "type") == responseevents.Create {
				if err := s.execute(message.body, nil); err != nil {
					return err
				}
			} else if err := s.control(message.body); err != nil {
				return err
			}
		case message := <-s.providerMessages:
			if err := webSocketMessageReadError(s.ctx, message); err != nil {
				return err
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if responseevents.StringField(fields, "type") == responseevents.Created {
				if err := s.execute(nil, message.body); err != nil {
					return err
				}
			} else if err := s.providerControl(message.body); err != nil {
				return err
			}
		}
	}
}

func (s *responsesWebSocket) control(body []byte) error {
	if isWebSocketSteering(body) {
		transportcapture.ObserveResponsesWebSocketControl(s.ctx, transportcapture.ResponsesWebSocketControlCodex, transportcapture.ResponsesWebSocketControlRequest, body)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	if responseevents.StringField(fields, "type") == responseevents.Create {
		if s.queuedCreate != nil {
			return incompatibleRequest("invalid_websocket_request", "a Responses WebSocket continuation is already queued")
		}
		s.queuedCreate = bytes.Clone(body)
		return nil
	}
	if s.upstream == nil {
		return incompatibleRequest("invalid_websocket_request", "send response.create before a WebSocket control message; Chat Completions providers do not support steering")
	}
	if responseevents.StringField(fields, "type") == responseevents.Steer {
		if err := s.history.AddSteering(fields); err != nil {
			return webSocketProtocolError(err)
		}
	}
	// Steering is user input, not an inference request. Forward it exactly once,
	// without changing its fields or treating an acknowledgement as completion.
	if err := s.upstream.Write(s.ctx, websocket.MessageText, body); err != nil {
		return err
	}
	if isWebSocketSteering(body) {
		transportcapture.ObserveResponsesWebSocketControl(s.ctx, transportcapture.ResponsesWebSocketControlProvider, transportcapture.ResponsesWebSocketControlRequest, body)
	}
	return nil
}

func isWebSocketSteering(body []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &event)
	return responseevents.Kind(event.Type).Steering()
}

func (s *responsesWebSocket) observeProvider(ctx context.Context, observation *transportcapture.WebSocketAttempt, body []byte) {
	if isWebSocketSteering(body) {
		transportcapture.ObserveResponsesWebSocketControl(ctx, transportcapture.ResponsesWebSocketControlProvider, transportcapture.ResponsesWebSocketControlResponse, body)
	} else {
		observation.Message(body)
	}
	s.history.ObserveControl(body)
}

func (s *responsesWebSocket) providerControl(body []byte) error {
	s.observeProvider(s.ctx, nil, body)
	if err := s.downstream.Write(s.ctx, websocket.MessageText, body); err != nil {
		return err
	}
	if isWebSocketSteering(body) {
		transportcapture.ObserveResponsesWebSocketControl(s.ctx, transportcapture.ResponsesWebSocketControlCodex, transportcapture.ResponsesWebSocketControlResponse, body)
	}
	return nil
}

func (s *responsesWebSocket) execute(command, firstEvent []byte) error {
	state, parsed, err := s.history.Prepare(command, firstEvent)
	if err != nil {
		return webSocketProtocolError(err)
	}
	headers := s.headers.Clone()
	var metadata map[string]json.RawMessage
	if json.Unmarshal(parsed.Fields["client_metadata"], &metadata) == nil {
		for _, key := range []string{codexresponses.TurnMetadataHeader, "x-codex-turn-state", codexresponses.ThreadIDHeader, codexresponses.WindowIDHeader, codexresponses.SubagentHeader} {
			if value, ok := responseevents.DecodeString(metadata[key]); ok {
				headers.Set(key, value)
			}
		}
	}
	exchange := &webSocketExchange{session: s, state: state, first: firstEvent}
	captureCtx, clientObservation := transportcapture.BeginResponsesWebSocket(s.ctx, headers, command)
	exchange.clientObservation = clientObservation
	startCtx, executionCtx, cancel := requestContexts(captureCtx, s.ctx, s.timeout)
	defer cancel()
	exchange.ctx = executionCtx
	output := &webSocketOutput{exchange: exchange}
	executor := requestExecutor{provider: exchange, output: output, issues: s.issues, mekugiCalls: s.proxy, serviceTiers: s.provider.serviceTiers, titleGenerator: s.provider.titleGenerator}
	err = executor.execute(startCtx, executionCtx, parsed, headers, routingSessionID(headers, parsed))
	if err != nil && executionCtx.Err() == nil && !s.errorDelivered {
		if payload, writeErr := s.writeError(executionCtx, err); writeErr == nil {
			clientObservation.Message(payload)
		}
	}
	exchange.observation.Finish(err)
	clientObservation.Finish(err)
	return err
}

type webSocketExchange struct {
	streamDiagnostics *transportcapture.Stream
	session           *responsesWebSocket
	state             *codexresponses.SessionExchange
	ctx               context.Context
	first             []byte
	clientObservation *transportcapture.ResponsesWebSocket
	observation       *transportcapture.WebSocketAttempt
	buffer            bytes.Buffer
	ended             bool
}

func (e *webSocketExchange) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	s := e.session
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	// Retain the actual submitted projection, not the native history. A later
	// terminal event is required before it can authorize incremental reuse.
	if err := e.state.BeginProvider(fields); err != nil {
		return nil, webSocketProtocolError(err)
	}
	previous := fields["previous_response_id"]
	delete(fields, "previous_response_id")
	native, err := responseevents.MarshalJSON(fields)
	if err != nil {
		return nil, err
	}
	if isChatCompletionsModel(responseevents.StringField(fields, "model")) {
		if string(fields["generate"]) == "false" {
			// Chat Completions has no non-generating transport warmup. Preserve Codex's
			// prewarm/history handshake without running and discarding inference.
			response := map[string]any{"id": "resp_mekugi_warm_" + rand.Text(), "status": "completed", "output": []any{}}
			payload := responseevents.MustMarshalJSON(map[string]any{"type": responseevents.Completed, "response": response})
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(bytes.NewReader(append(append([]byte("data: "), payload...), '\n', '\n')))}, nil
		}
		return s.provider.forwardExecution(startCtx, responseCtx, native, headers, cacheKey)
	}
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, err
	}
	upstreamHeaders := make(http.Header)
	upstreamHeaders.Set("Authorization", authorization)
	upstreamHeaders.Set(openai.AccountIDHeader, account)
	upstreamHeaders.Set("Originator", codexresponses.ClientIdentity)
	upstreamHeaders.Set("User-Agent", codexresponses.ClientIdentity)
	codexresponses.ForwardHeaders(upstreamHeaders, headers)
	payload, handshake, _, err := codexresponses.WebSocketRequest(native, upstreamHeaders, cacheKey)
	if err != nil {
		return nil, err
	}
	if len(previous) != 0 {
		payload, err = responseevents.ReplaceField(payload, "previous_response_id", previous)
		if err != nil {
			return nil, err
		}
	}
	var responseHeaders http.Header
	if s.upstream == nil {
		conn, response, dialErr := s.provider.openai.Dial(startCtx, handshake)

		if rejected := response; rejected != nil && rejected.StatusCode != http.StatusSwitchingProtocols {
			attempt := transportcapture.BeginWebSocketAttempt(responseCtx, nil, handshake, rejected.Header)
			body := attempt.HTTPErrorBody(rejected.Body, rejected.StatusCode, rejected.Header.Get("Content-Type"), rejected.Header.Get("Content-Encoding"))
			// A truncated error body cannot erase an already-known status used
			// by Codex for authentication recovery or rate-limit handling.
			payload, _ := io.ReadAll(io.LimitReader(body, maxUpstreamErrorDetailBytes))
			_ = body.Close()
			return nil, &webSocketStatusError{status: rejected.StatusCode, body: payload}
		}
		if dialErr != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			return nil, fmt.Errorf("connect provider WebSocket: %w", dialErr)
		}
		s.upstream = conn
		conn.SetReadLimit(codexresponses.MaxResponseBytes)
		responseHeaders = response.Header.Clone()
		s.providerMessages = readResponsesWebSocket(s.ctx, conn, nil)
	}
	captured := payload
	if e.state.Automatic() {
		captured = nil
	}
	if e.streamDiagnostics != nil {
		if id := responseHeaders.Get("X-Request-Id"); responseevents.SafeDiagnosticIdentity(id) {
			e.streamDiagnostics.ProviderRequestID = id
		}
	}
	e.observation = transportcapture.BeginWebSocketAttempt(responseCtx, captured, handshake, responseHeaders)
	if !e.state.Automatic() {
		if err := s.upstream.Write(startCtx, websocket.MessageText, payload); err != nil {
			return nil, err
		}
	}
	// The downstream upgrade precedes the provider connection, so its headers
	// cannot carry the provider's sticky routing state. Deliver it through the
	// metadata event Codex consumes, leaving subsequent replay and reset to Codex.
	// Source: codex-rs/codex-api/src/sse/responses.rs:222:230 (turn_state).
	if state := responseHeaders.Get("x-codex-turn-state"); state != "" {
		metadata := responseevents.MustMarshalJSON(map[string]any{
			"type": "response.metadata", "headers": map[string]string{"x-codex-turn-state": state},
		})
		if err := s.downstream.Write(responseCtx, websocket.MessageText, metadata); err != nil {
			return nil, err
		}
		e.clientObservation.Message(metadata)
	}
	// Wait for actual response admission, not just the successful handshake.
	// Meanwhile the downstream reader stays live and can deliver steering.
	for len(e.first) == 0 {
		select {
		case <-startCtx.Done():
			return nil, startCtx.Err()
		case message := <-s.clientMessages:
			if err := webSocketMessageReadError(responseCtx, message); err != nil {
				return nil, err
			}
			if err := s.control(message.body); err != nil {
				return nil, err
			}
		case message := <-s.providerMessages:
			if err := webSocketMessageReadError(responseCtx, message); err != nil {
				return nil, err
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if isWebSocketSteering(message.body) {
				if err := s.providerControl(message.body); err != nil {
					return nil, err
				}
				continue
			}
			if responseevents.Kind(responseevents.StringField(fields, "type")).Ancillary() {
				e.streamDiagnostics.Observe(message.body)
				e.observation.Message(message.body)
				e.clientObservation.Message(message.body)
				if err := s.providerControl(message.body); err != nil {
					return nil, err
				}
				continue
			}
			e.first = message.body
		}
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: e}, nil
}

func (e *webSocketExchange) Read(buffer []byte) (int, error) {
	if e.buffer.Len() != 0 {
		return e.buffer.Read(buffer)
	}
	if e.ended {
		return 0, io.EOF
	}
	s := e.session
	body := e.first
	e.first = nil
	var timer *time.Timer
	var idle <-chan time.Time
	if s.provider.openai.StreamIdleTimeout > 0 {
		timer = time.NewTimer(s.provider.openai.StreamIdleTimeout)
		idle = timer.C
		defer timer.Stop()
	}
	for len(body) == 0 {
		select {
		case <-e.ctx.Done():
			recordStreamReadEnd(e.streamDiagnostics, e.ctx.Err(), "context")
			return 0, e.ctx.Err()
		case <-idle:
			return 0, httptransport.ErrStreamIdleTimeout
		case message := <-s.clientMessages:
			if err := webSocketMessageReadError(e.ctx, message); err != nil {
				recordStreamReadEnd(e.streamDiagnostics, err, "downstream")
				return 0, err
			}
			if err := s.control(message.body); err != nil {
				recordStreamReadEnd(e.streamDiagnostics, err, "router_control")
				return 0, err
			}
		case message := <-s.providerMessages:
			if err := webSocketMessageReadError(e.ctx, message); err != nil {
				recordStreamReadEnd(e.streamDiagnostics, err, "upstream")
				return 0, err
			}
			body = message.body
		}
	}
	s.observeProvider(e.ctx, e.observation, body)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	kind := responseevents.StringField(fields, "type")
	if err := e.state.ObserveProvider(body); err != nil {
		recordStreamReadEnd(e.streamDiagnostics, err, "router_history")
		return 0, err
	}
	e.ended = responseevents.Kind(kind).EndsExchange()
	e.buffer.WriteString("data: ")
	for index, line := range bytes.Split(body, []byte("\n")) {
		if index != 0 {
			e.buffer.WriteString("\ndata: ")
		}
		e.buffer.Write(line)
	}
	e.buffer.WriteString("\n\n")
	return e.buffer.Read(buffer)
}

func (e *webSocketExchange) Close() error { return nil }

// executeRequest retains the normal SSE transformer composition. This final
// adapter changes framing only, after tool restoration and replay.
type webSocketOutput struct {
	exchange  *webSocketExchange
	buffer    bytes.Buffer
	committed bool
}

func (w *webSocketOutput) Write(data []byte) (int, error) {
	w.buffer.Write(data)
	for {
		raw := w.buffer.Bytes()
		end := bytes.Index(raw, []byte("\n\n"))
		if end < 0 {
			return len(data), nil
		}
		payload := codexresponses.SSEPayload(strings.SplitAfter(string(raw[:end]), "\n"))
		w.buffer.Next(end + 2)
		if len(payload) == 0 {
			continue
		}
		if err := w.message(payload); err != nil {
			return 0, err
		}
	}
}

func (w *webSocketOutput) message(payload []byte) error {
	e := w.exchange
	s := e.session
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if err := e.state.ObserveOutput(payload); err != nil {
		return err
	}
	if event.Type == responseevents.Error {
		s.errorDelivered = true
	}
	if err := s.downstream.Write(e.ctx, websocket.MessageText, payload); err != nil {
		return downstreamDisconnectError(err)
	}
	w.committed = true
	if isWebSocketSteering(payload) {
		transportcapture.ObserveResponsesWebSocketControl(e.ctx, transportcapture.ResponsesWebSocketControlCodex, transportcapture.ResponsesWebSocketControlResponse, payload)
	} else {
		e.clientObservation.Message(payload)
	}
	return nil
}

// Map backend admission failures to the application's compatibility diagnostic.
func webSocketProtocolError(err error) error {
	if invalid, ok := errors.AsType[*codexresponses.InvalidRequestError](err); ok {
		return incompatibleRequest("invalid_websocket_request", invalid.Message)
	}
	return err
}
