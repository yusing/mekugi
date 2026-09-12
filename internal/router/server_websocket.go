package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/capturer"
)

// A downstream socket owns a dedicated provider socket. In particular it never
// enters the HTTP pool: accepted steering and previous_response_id are scoped
// to this connection, including the quiet interval after response.completed.
func responsesWebSocketHandler(lifecycle context.Context, timeout time.Duration, provider *providerClient, issues *CriticalErrors, proxy *mekugiProxy, codec *ctp2Codec, mentor *mentorHandoff) *responsesWebSocketEndpoint {
	lifecycle, cancel := context.WithCancel(lifecycle)
	endpoint := &responsesWebSocketEndpoint{cancel: cancel}
	endpoint.handler = func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := requiredCodexAuthHeaders(r.Header); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(responsesRequestBufferBytes)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(lifecycle, cancel)
		defer stop()
		s := &responsesWebSocket{
			ctx: ctx, downstream: conn, provider: provider, headers: r.Header.Clone(),
			timeout: timeout, issues: issues, proxy: proxy, codec: codec, mentor: mentor,
			clientMessages: readResponsesWebSocket(ctx, conn, cancel), histories: make(map[string]*webSocketHistory),
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
	return fmt.Sprintf("provider WebSocket upgrade returned HTTP %d", e.status)
}

func (s *responsesWebSocket) writeError(ctx context.Context, err error) ([]byte, error) {
	status := http.StatusBadGateway
	if _, ok := errors.AsType[*requestCompatibilityError](err); ok {
		status = http.StatusBadRequest
	}
	fields := map[string]any{"type": "error", "status": status, "error": map[string]string{"type": "invalid_request_error", "message": err.Error()}}
	if upstream, ok := errors.AsType[*webSocketStatusError](err); ok {
		fields["status"] = upstream.status
		var body map[string]json.RawMessage
		if json.Unmarshal(upstream.body, &body) == nil && len(body["error"]) != 0 {
			fields["error"] = body["error"]
		}
	}
	body := mustMarshalJSON(fields)
	writeErr := s.downstream.Write(ctx, websocket.MessageText, body)
	s.errorDelivered = writeErr == nil
	return body, writeErr
}

type webSocketMessage struct {
	body []byte
	err  error
}

func readResponsesWebSocket(ctx context.Context, conn *websocket.Conn, disconnected context.CancelFunc) <-chan webSocketMessage {
	messages := make(chan webSocketMessage, 1)
	go func() {
		defer close(messages)
		for {
			kind, body, err := conn.Read(ctx)
			if err != nil && disconnected != nil {
				disconnected()
			}
			readFailed := err != nil
			if err == nil && kind != websocket.MessageText {
				err = errors.New("Responses WebSocket requires text JSON messages")
			}
			if err == nil && !json.Valid(body) {
				err = errors.New("Responses WebSocket received invalid JSON")
			}
			if !readFailed && err != nil && disconnected != nil {
				err = incompatibleRequest("invalid_websocket_request", err.Error())
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

// Histories share their immutable ancestors rather than retaining quadratic
// copies of a growing conversation. Only native client-visible items are kept;
// model-visible calls and CTP sources are rebuilt by the ordinary preparation.
type webSocketHistory struct {
	parent   *webSocketHistory
	input    []json.RawMessage
	output   []json.RawMessage
	settings map[string]json.RawMessage
	// Automatic steering successors execute the already-sent model contract,
	// even if the preceding terminal completed the Mentor schedule.
	providerModel     string
	providerReasoning json.RawMessage
	// Fingerprint only instruction-bearing input actually sent upstream. Native
	// history cannot establish whether its later projection matches that cache.
	instructionDigest [sha256.Size]byte
}

func instructionInputDigest(input []json.RawMessage, digest [sha256.Size]byte) ([sha256.Size]byte, error) {
	for _, raw := range input {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			return digest, err
		}
		role := jsonString(item, "role")
		if role != "developer" && role != "system" && jsonString(item, "type") != "additional_tools" {
			continue
		}
		// Normalize object order and whitespace without rounding JSON numbers.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return digest, err
		}
		digest = sha256.Sum256(append(digest[:], mustMarshalJSON(value)...))
	}
	return digest, nil
}

func (e *webSocketExchange) prepareInstructionCache(request *parsedResponsesRequest, body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	input, err := webSocketInput(fields["input"])
	if err != nil || request.cachedInput > len(input) {
		return errors.New("invalid WebSocket instruction cache boundary")
	}
	prefix, err := instructionInputDigest(input[:request.cachedInput], [sha256.Size]byte{})
	if err != nil {
		return err
	}
	if request.cachedInput != 0 && !isGrokModel(request.model()) && prefix != e.cachedInstructionDigest {
		if e.automatic {
			return errors.New("automatic WebSocket successor changed cached instructions")
		}
		// A cached prefix is immutable upstream. Send a new full-context request
		// instead of silently dropping edits to the inherited instructions/tools.
		request.cachedInput = 0
		request.rebaseInput = true
	}
	e.history.instructionDigest, err = instructionInputDigest(input, [sha256.Size]byte{})
	return err
}

func (h *webSocketHistory) items() []json.RawMessage {
	var ancestry []*webSocketHistory
	for node := h; node != nil; node = node.parent {
		ancestry = append(ancestry, node)
	}
	var items []json.RawMessage
	for _, a := range slices.Backward(ancestry) {
		items = append(items, a.input...)
		items = append(items, a.output...)
	}
	return items
}

type webSocketSteer struct {
	id, parent string
	input      []json.RawMessage
}

type responsesWebSocket struct {
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
	codec            *ctp2Codec
	mentor           *mentorHandoff
	histories        map[string]*webSocketHistory
	lastID           string
	steers           []webSocketSteer
	queuedCreate     []byte
	errorDelivered   bool
	retainedBytes    int
}

func (s *responsesWebSocket) retain(items []json.RawMessage) error {
	for _, item := range items {
		s.retainedBytes += len(item)
	}
	if s.retainedBytes > upstreamJSONBufferBytes {
		return errors.New("Responses WebSocket history exceeds the router buffer budget")
	}
	return nil
}

func webSocketInput(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []json.RawMessage{mustMarshalJSON(map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]string{"type": "input_text", "text": text}},
		})}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, incompatibleRequest("invalid_websocket_request", "decode WebSocket input: "+err.Error())
	}
	return items, nil
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
			if message.err != nil {
				return message.err
			}
			if len(message.body) == 0 {
				return io.EOF
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if jsonString(fields, "type") == "response.create" {
				if err := s.execute(message.body, nil); err != nil {
					return err
				}
			} else if err := s.control(message.body); err != nil {
				return err
			}
		case message := <-s.providerMessages:
			if message.err != nil {
				return message.err
			}
			if len(message.body) == 0 {
				return io.EOF
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if jsonString(fields, "type") == "response.created" {
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
		capturer.ObserveResponsesWebSocketControl(s.ctx, capturer.ResponsesWebSocketControlCodex, capturer.ResponsesWebSocketControlRequest, body)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	if jsonString(fields, "type") == "response.create" {
		if s.queuedCreate != nil {
			return incompatibleRequest("invalid_websocket_request", "a Responses WebSocket continuation is already queued")
		}
		s.queuedCreate = bytes.Clone(body)
		return nil
	}
	if s.upstream == nil {
		return incompatibleRequest("invalid_websocket_request", "send response.create before a WebSocket control message; Grok does not support steering")
	}
	if jsonString(fields, "type") == "response.steer" {
		input, err := webSocketInput(fields["input"])
		if err != nil {
			return err
		}
		if err := s.retain(input); err != nil {
			return err
		}
		s.steers = append(s.steers, webSocketSteer{parent: jsonString(fields, "previous_response_id"), input: input})
	}
	// Steering is user input, not an inference request. Forward it exactly once,
	// without changing its fields or treating an acknowledgement as completion.
	if err := s.upstream.Write(s.ctx, websocket.MessageText, body); err != nil {
		return err
	}
	if isWebSocketSteering(body) {
		capturer.ObserveResponsesWebSocketControl(s.ctx, capturer.ResponsesWebSocketControlProvider, capturer.ResponsesWebSocketControlRequest, body)
	}
	return nil
}

func (s *responsesWebSocket) observeControl(body []byte) {
	var event struct {
		Type  string `json:"type"`
		Steer struct {
			ID     string `json:"id"`
			Parent string `json:"previous_response_id"`
		} `json:"steer"`
	}
	if json.Unmarshal(body, &event) != nil {
		return
	}
	for index := range s.steers {
		steer := &s.steers[index]
		switch event.Type {
		case "response.steer.accepted":
			if steer.id == "" && steer.parent == event.Steer.Parent {
				steer.id = event.Steer.ID
				return
			}
		case "response.steer.failed":
			if event.Steer.ID != "" && steer.id == event.Steer.ID ||
				event.Steer.ID == "" && steer.id == "" && steer.parent == event.Steer.Parent {
				s.steers = append(s.steers[:index], s.steers[index+1:]...)
				return
			}
		}
	}
}

// Acknowledgement/failure can arrive after a continuation was sent. Admission,
// not preparation's snapshot, owns native history. Legal steering contains user
// messages, never CTP tool-output sources or developer instruction/tool carriers.
func (s *responsesWebSocket) commitSteering(history *webSocketHistory, parent string) {
	var committed []json.RawMessage
	remaining := s.steers[:0]
	for _, steer := range s.steers {
		if steer.id == "" || steer.parent != parent {
			remaining = append(remaining, steer)
		} else {
			committed = append(committed, steer.input...)
		}
	}
	s.steers = remaining
	history.input = append(committed, history.input...)
}

func isWebSocketSteering(body []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &event)
	return event.Type == "response.steer" || strings.HasPrefix(event.Type, "response.steer.")
}

func (s *responsesWebSocket) observeProvider(ctx context.Context, observation *capturer.WebSocketAttempt, body []byte) {
	if isWebSocketSteering(body) {
		capturer.ObserveResponsesWebSocketControl(ctx, capturer.ResponsesWebSocketControlProvider, capturer.ResponsesWebSocketControlResponse, body)
	} else {
		observation.Message(body)
	}
	s.observeControl(body)
}

func (s *responsesWebSocket) providerControl(body []byte) error {
	s.observeProvider(s.ctx, nil, body)
	if err := s.downstream.Write(s.ctx, websocket.MessageText, body); err != nil {
		return err
	}
	if isWebSocketSteering(body) {
		capturer.ObserveResponsesWebSocketControl(s.ctx, capturer.ResponsesWebSocketControlCodex, capturer.ResponsesWebSocketControlResponse, body)
	}
	return nil
}

func (s *responsesWebSocket) execute(command, firstEvent []byte) error {
	automatic := len(command) == 0
	var fields map[string]json.RawMessage
	if !automatic {
		if err := json.Unmarshal(command, &fields); err != nil || fields == nil {
			return incompatibleRequest("invalid_websocket_request", "invalid response.create")
		}
		if value := fields["stream_id"]; len(value) != 0 && string(value) != "null" {
			return incompatibleRequest("invalid_websocket_request", "multiplexed Responses WebSocket lanes are not supported")
		}
	} else {
		var event struct {
			Response struct {
				Parent string `json:"previous_response_id"`
			} `json:"response"`
		}
		_ = json.Unmarshal(firstEvent, &event)
		parent := event.Response.Parent
		if parent == "" {
			parent = s.lastID
		}
		fields = map[string]json.RawMessage{"previous_response_id": mustMarshalJSON(parent)}
	}
	parentID := jsonString(fields, "previous_response_id")
	var parent *webSocketHistory
	if parentID != "" {
		parent = s.histories[parentID]
		if parent == nil {
			if automatic {
				return errors.New("provider automatic response names an unretained previous_response_id")
			}
			return incompatibleRequest("invalid_websocket_request", "previous_response_id is not retained on this Responses WebSocket")
		}
	}
	if !automatic {
		s.retainedBytes += len(command)
		if s.retainedBytes > upstreamJSONBufferBytes {
			return errors.New("Responses WebSocket history exceeds the router buffer budget")
		}
	}
	input, err := webSocketInput(fields["input"])
	if err != nil {
		return err
	}
	if err := s.retain(input); err != nil {
		return err
	}
	var steering []json.RawMessage
	for _, steer := range s.steers {
		if steer.id != "" && steer.parent == parentID {
			steering = append(steering, steer.input...)
		}
	}
	settings := make(map[string]json.RawMessage)
	if automatic && parent != nil {
		maps.Copy(settings, parent.settings)
	}
	maps.Copy(settings, fields)
	delete(settings, "type")
	delete(settings, "input")
	delete(settings, "previous_response_id")
	// generate=false is a prewarm operation, never an inherited setting.
	delete(settings, "generate")
	requestFields := maps.Clone(settings)
	if generate, ok := fields["generate"]; ok {
		requestFields["generate"] = generate
	}
	var fullInput []json.RawMessage
	if parent != nil {
		fullInput = parent.items()
	}
	fullInput = append(fullInput, steering...)
	cachedInput := len(fullInput)
	fullInput = append(fullInput, input...)
	requestFields["input"] = mustMarshalJSON(fullInput)
	requestFields["stream"] = json.RawMessage("true")
	if parentID != "" {
		requestFields["previous_response_id"] = mustMarshalJSON(parentID)
	}
	body, err := marshalProtocolJSON(requestFields)
	if err != nil {
		return err
	}
	if len(body) > responsesRequestBufferBytes {
		return errors.New("Responses WebSocket reconstructed request exceeds the router buffer budget")
	}
	parsed, err := parseResponsesRequest(body)
	if err != nil {
		if automatic {
			return err
		}
		return incompatibleRequest("invalid_websocket_request", err.Error())
	}
	parsed.cachedInput = cachedInput
	headers := s.headers.Clone()
	var metadata map[string]json.RawMessage
	if json.Unmarshal(requestFields["client_metadata"], &metadata) == nil {
		for _, key := range []string{codexTurnMetadataHeader, "x-codex-turn-state", threadIDHeader, codexWindowIDHeader, openAISubagentHeader} {
			if value, ok := decodeJSONString(metadata[key]); ok {
				headers.Set(key, value)
			}
		}
	}
	history := &webSocketHistory{parent: parent, input: input, settings: settings}
	exchange := &webSocketExchange{session: s, automatic: automatic, first: firstEvent, history: history, parentID: parentID}
	if parent != nil {
		exchange.cachedInstructionDigest = parent.instructionDigest
	}
	exchange.cachedInstructionDigest, err = instructionInputDigest(steering, exchange.cachedInstructionDigest)
	if err != nil {
		return err
	}
	captureCtx, clientObservation := capturer.BeginResponsesWebSocket(s.ctx, headers, command)
	exchange.clientObservation = clientObservation
	startCtx, executionCtx, cancel := requestContexts(captureCtx, s.ctx, s.timeout)
	defer cancel()
	exchange.ctx = executionCtx
	output := &webSocketOutput{exchange: exchange}
	err = executeRequest(startCtx, executionCtx, parsed, headers, routingSessionID(headers, parsed), exchange, output, s.issues, s.proxy, s.codec, s.mentor)
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
	session                 *responsesWebSocket
	ctx                     context.Context
	automatic               bool
	first                   []byte
	history                 *webSocketHistory
	parentID                string
	clientObservation       *capturer.ResponsesWebSocket
	observation             *capturer.WebSocketAttempt
	buffer                  bytes.Buffer
	ended                   bool
	cachedInstructionDigest [sha256.Size]byte
}

func (e *webSocketExchange) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	s := e.session
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	previous := fields["previous_response_id"]
	delete(fields, "previous_response_id")
	native, err := marshalProtocolJSON(fields)
	if err != nil {
		return nil, err
	}
	if isGrokModel(jsonString(fields, "model")) {
		if string(fields["generate"]) == "false" {
			// Grok has no non-generating transport warmup. Preserve Codex's
			// prewarm/history handshake without running and discarding inference.
			response := map[string]any{"id": "resp_mekugi_warm_" + rand.Text(), "status": "completed", "output": []any{}}
			payload := mustMarshalJSON(map[string]any{"type": "response.completed", "response": response})
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(bytes.NewReader(append(append([]byte("data: "), payload...), '\n', '\n')))}, nil
		}
		return s.provider.forwardExecution(startCtx, responseCtx, native, headers, cacheKey)
	}
	authorization, account, err := requiredCodexAuthHeaders(headers)
	if err != nil {
		return nil, err
	}
	upstreamHeaders := make(http.Header)
	upstreamHeaders.Set("Authorization", authorization)
	upstreamHeaders.Set(chatGPTAccountIDHeader, account)
	upstreamHeaders.Set("Originator", codexClientIdentity)
	upstreamHeaders.Set("User-Agent", codexClientIdentity)
	forwardCodexRequestHeaders(upstreamHeaders, headers)
	payload, handshake, _, err := webSocketRequest(native, upstreamHeaders, cacheKey)
	if err != nil {
		return nil, err
	}
	if len(previous) != 0 {
		payload, err = replaceRawField(payload, "previous_response_id", previous)
		if err != nil {
			return nil, err
		}
	}
	var responseHeaders http.Header
	if s.upstream == nil {
		client := *s.provider.httpClient
		client.Timeout = 0
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		transport := &webSocketHandshakeTransport{RoundTripper: client.Transport}
		client.Transport = transport
		conn, response, dialErr := websocket.Dial(startCtx, strings.TrimRight(s.provider.baseURL, "/")+"/responses", &websocket.DialOptions{
			HTTPClient: &client, HTTPHeader: handshake,
		})

		if rejected := transport.rejected; rejected != nil {
			attempt := capturer.BeginWebSocketAttempt(responseCtx, nil, handshake, rejected.Header)
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
		conn.SetReadLimit(upstreamJSONBufferBytes)
		responseHeaders = response.Header.Clone()
		s.providerMessages = readResponsesWebSocket(s.ctx, conn, nil)
	}
	captured := payload
	if e.automatic {
		captured = nil
	}
	e.observation = capturer.BeginWebSocketAttempt(responseCtx, captured, handshake, responseHeaders)
	if !e.automatic {
		if err := s.upstream.Write(startCtx, websocket.MessageText, payload); err != nil {
			return nil, err
		}
	}
	// Wait for actual response admission, not just the successful handshake.
	// Meanwhile the downstream reader stays live and can deliver steering.
	for len(e.first) == 0 {
		select {
		case <-startCtx.Done():
			return nil, startCtx.Err()
		case message := <-s.clientMessages:
			if message.err != nil {
				return nil, message.err
			}
			if len(message.body) == 0 {
				return nil, io.EOF
			}
			if err := s.control(message.body); err != nil {
				return nil, err
			}
		case message := <-s.providerMessages:
			if message.err != nil {
				return nil, message.err
			}
			if len(message.body) == 0 {
				return nil, io.EOF
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(message.body, &fields)
			if isWebSocketSteering(message.body) {
				if err := s.providerControl(message.body); err != nil {
					return nil, err
				}
				continue
			}
			if isResponseAncillaryEvent(jsonString(fields, "type")) {
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
	if s.provider.streamIdleTimeout > 0 {
		timer = time.NewTimer(s.provider.streamIdleTimeout)
		idle = timer.C
		defer timer.Stop()
	}
	for len(body) == 0 {
		select {
		case <-e.ctx.Done():
			return 0, e.ctx.Err()
		case <-idle:
			return 0, errUpstreamStreamIdleTimeout
		case message := <-s.clientMessages:
			if message.err != nil {
				return 0, message.err
			}
			if len(message.body) == 0 {
				return 0, io.EOF
			}
			if err := s.control(message.body); err != nil {
				return 0, err
			}
		case message := <-s.providerMessages:
			if message.err != nil {
				return 0, message.err
			}
			if len(message.body) == 0 {
				return 0, io.EOF
			}
			body = message.body
		}
	}
	s.observeProvider(e.ctx, e.observation, body)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	kind := jsonString(fields, "type")
	e.ended = kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" || kind == "error"
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
// adapter changes framing only, after CTP decoding, tool restoration and replay.
type webSocketOutput struct {
	exchange *webSocketExchange
	buffer   bytes.Buffer
}

func (w *webSocketOutput) Write(data []byte) (int, error) {
	w.buffer.Write(data)
	for {
		raw := w.buffer.Bytes()
		end := bytes.Index(raw, []byte("\n\n"))
		if end < 0 {
			return len(data), nil
		}
		event := bytes.Clone(raw[:end])
		w.buffer.Next(end + 2)
		payload := ssePayload(strings.SplitAfter(string(event), "\n"))
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
		Type     string `json:"type"`
		Response struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.Type == "error" {
		s.errorDelivered = true
	}
	if event.Type == "response.output_item.done" {
		var item struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(payload, &item) == nil && len(item.Item) != 0 {
			if err := s.retain([]json.RawMessage{item.Item}); err != nil {
				return err
			}
			e.history.output = append(e.history.output, item.Item)
		}
	}
	if event.Type == "response.created" {
		// Creation commits queued steering. Never replay accepted input after
		// this point, even if delivery or the successor subsequently fails.
		s.commitSteering(e.history, e.parentID)
	}
	switch event.Type {
	case "response.completed", "response.incomplete", "response.failed":
		if event.Response.ID == "" {
			return errors.New("provider terminal response has no id")
		}
		if len(event.Response.Output) != 0 {
			// Terminal snapshots can omit completed streamed calls, especially
			// when an empty provider output gains router commentary. Reconcile
			// matching items without discarding the host's completed history.
			output := mergeWebSocketOutput(e.history.output, event.Response.Output)
			retained := s.retainedBytes
			for _, item := range e.history.output {
				s.retainedBytes -= len(item)
			}
			if err := s.retain(output); err != nil {
				s.retainedBytes = retained
				return err
			}
			e.history.output = output
		}
		s.histories[event.Response.ID] = e.history
		s.lastID = event.Response.ID
	}
	if err := s.downstream.Write(e.ctx, websocket.MessageText, payload); err != nil {
		return err
	}
	if isWebSocketSteering(payload) {
		capturer.ObserveResponsesWebSocketControl(e.ctx, capturer.ResponsesWebSocketControlCodex, capturer.ResponsesWebSocketControlResponse, payload)
	} else {
		e.clientObservation.Message(payload)
	}
	return nil
}

// Keep delivered order while accepting final metadata for matching identities.
// Items without an ID can only be deduplicated by their exact retained encoding.
func mergeWebSocketOutput(streamed, terminal []json.RawMessage) []json.RawMessage {
	output := slices.Clone(streamed)
	positions := make(map[string]int, len(streamed)+len(terminal))
	key := func(raw json.RawMessage) string {
		var item struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &item) == nil && item.ID != "" {
			return "id:" + item.ID
		}
		return "raw:" + string(raw)
	}
	for index, item := range streamed {
		positions[key(item)] = index
	}
	for _, item := range terminal {
		identity := key(item)
		if index, exists := positions[identity]; exists {
			output[index] = item
		} else {
			positions[identity] = len(output)
			output = append(output, item)
		}
	}
	return output
}
