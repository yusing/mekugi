package router

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/capturer"
)

var errProviderWebSocketResponse = errors.New("provider websocket error")

const (
	providerWebSocketLimit    = 32
	providerWebSocketIdle     = time.Minute
	providerWebSocketLifetime = 50 * time.Minute
	responsesWebSocketBeta    = "responses_websockets=2026-02-06"
)

// The pool owns connections, not conversation state. Every lease sends the full
// input and has its own body, cancellation, capture, and terminal-event boundary.
// Busy connections never multiplex responses. Idle connections are evictable.
type providerWebSockets struct {
	ctx      context.Context
	client   *http.Client
	endpoint string
	mu       sync.Mutex
	entries  map[*providerWebSocket]struct{}
	changed  chan struct{}
	closed   bool
}

type providerWebSocket struct {
	pool         *providerWebSockets
	key          [32]byte
	conn         *websocket.Conn
	born         time.Time
	busy         bool
	idleTimer    *time.Timer
	lease        *providerWebSocketLease
	receiverDone chan struct{}
	done         chan struct{}
	err          error
	cancel       context.CancelFunc
}

// A queue, delivery reservation, and observer belong to a lease, never to a
// reusable connection. A late receiver or cancellation cannot target its successor.
type providerWebSocketLease struct {
	messages    chan []byte
	released    chan struct{}
	observation *capturer.WebSocketAttempt
	pending     bool
	terminal    bool
}

func newProviderWebSocketLease(observation *capturer.WebSocketAttempt) *providerWebSocketLease {
	return &providerWebSocketLease{messages: make(chan []byte, 1), released: make(chan struct{}), observation: observation}
}

func (c *providerClient) enableWebSockets(ctx context.Context) {
	c.websockets = &providerWebSockets{ctx: ctx, client: c.httpClient, endpoint: strings.TrimRight(c.baseURL, "/") + "/responses", entries: make(map[*providerWebSocket]struct{}), changed: make(chan struct{})}
	context.AfterFunc(ctx, c.websockets.close)
}

func (p *providerWebSockets) signalLocked() { close(p.changed); p.changed = make(chan struct{}) }

func (p *providerWebSockets) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for entry := range p.entries {
		p.discardLocked(entry, context.Canceled)
	}
	p.signalLocked()
}

func (p *providerWebSockets) discardLocked(entry *providerWebSocket, err error) {
	if _, present := p.entries[entry]; !present {
		return
	}
	delete(p.entries, entry)
	entry.err = err
	if entry.idleTimer != nil {
		entry.idleTimer.Stop()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.conn != nil {
		_ = entry.conn.CloseNow()
	}
	close(entry.done)
	p.signalLocked()
}

func (p *providerWebSockets) acquire(ctx context.Context, headers http.Header, begin func(http.Header) *capturer.WebSocketAttempt) (*providerWebSocket, http.Header, *http.Response, error) {
	encoded, _ := json.Marshal(headers)
	key := sha256.Sum256(encoded)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, nil, nil, context.Canceled
		}
		for entry := range p.entries {
			if !entry.busy && time.Since(entry.born) >= providerWebSocketLifetime {
				p.discardLocked(entry, errors.New("websocket lifetime expired"))
				continue
			}
			if !entry.busy && entry.key == key {
				entry.busy = true
				entry.lease = newProviderWebSocketLease(begin(nil))
				entry.idleTimer.Stop()
				p.mu.Unlock()
				// Handshake headers belong to the first exchange only. In particular its
				// request ID and sticky token must not be replayed on later responses.
				return entry, nil, nil, nil
			}
		}
		if len(p.entries) >= providerWebSocketLimit {
			var idle *providerWebSocket
			for entry := range p.entries {
				if !entry.busy && (idle == nil || entry.born.Before(idle.born)) {
					idle = entry
				}
			}
			if idle != nil {
				p.discardLocked(idle, errors.New("idle websocket evicted"))
			} else {
				changed := p.changed
				p.mu.Unlock()
				select {
				case <-changed:
					continue
				case <-ctx.Done():
					return nil, nil, nil, ctx.Err()
				}
			}
		}
		dialCtx, dialCancel := context.WithCancel(ctx)
		if p.client.Timeout > 0 {
			dialCancel()
			dialCtx, dialCancel = context.WithTimeout(ctx, p.client.Timeout)
		}
		entry := &providerWebSocket{cancel: dialCancel, pool: p, key: key, born: time.Now(), busy: true, receiverDone: make(chan struct{}), done: make(chan struct{})}
		p.entries[entry] = struct{}{}
		p.mu.Unlock()
		// Never follow a redirect with provider credentials or turn a rejected
		// upgrade into a successful request to a different endpoint.
		client := *p.client
		client.Timeout = 0 // The lease owns the deadline, including rejected response bodies.
		handshakeTransport := &webSocketHandshakeTransport{RoundTripper: client.Transport}
		client.Transport = handshakeTransport
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		conn, response, err := websocket.Dial(dialCtx, p.endpoint, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: headers})
		p.mu.Lock()
		if err != nil {
			if rejected := handshakeTransport.rejected; rejected != nil {
				// Dial truncates failed upgrade bodies. The transport retained the real
				// body and its cancellation, which now belong to the HTTP error path.
				entry.cancel = nil
				rejected.Body = &cancelOnCloseReadCloser{body: rejected.Body, cancel: dialCancel}
				response = rejected
			}
			p.discardLocked(entry, err)
			p.mu.Unlock()
			return nil, nil, response, err
		}
		if p.closed {
			_ = conn.CloseNow()
			p.mu.Unlock()
			return nil, nil, nil, context.Canceled
		}
		dialCancel()
		entry.conn = conn
		entry.lease = newProviderWebSocketLease(begin(response.Header))
		conn.SetReadLimit(upstreamJSONBufferBytes)
		readCtx, cancel := context.WithCancel(p.ctx)
		entry.cancel = cancel
		p.mu.Unlock()
		go entry.readLoop(readCtx)
		return entry, response.Header.Clone(), nil, nil
	}
}

func (entry *providerWebSocket) readLoop(ctx context.Context) {
	defer close(entry.receiverDone)
	for {
		entry.pool.mu.Lock()
		lease := entry.lease
		entry.pool.mu.Unlock()
		kind, payload, err := entry.conn.Read(ctx)
		if err == nil && kind != websocket.MessageText {
			err = errors.New("provider websocket sent a non-text message")
		}
		var event struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &event)
		entry.pool.mu.Lock()
		// An idle read may span acquisition of a new lease. Active reads retain
		// their original identity even when cancellation removes the connection.
		if lease == nil {
			lease = entry.lease
		}
		if lease != nil && len(payload) != 0 {
			lease.observation.Message(payload)
		}
		if err == nil && (!entry.busy || entry.lease != lease || lease == nil) {
			err = errors.New("provider websocket sent an unsolicited message")
		}
		if err != nil {
			entry.pool.discardLocked(entry, err)
			entry.pool.mu.Unlock()
			return
		}
		if _, present := entry.pool.entries[entry]; !present {
			entry.pool.mu.Unlock()
			return
		}
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete", "error":
			lease.terminal = true
		}
		// Admission is atomic with release/acquire. If the bounded queue is
		// full, keep a reservation tied to this lease while waiting to deliver.
		select {
		case lease.messages <- payload:
			entry.pool.mu.Unlock()
		default:
			lease.pending = true
			entry.pool.mu.Unlock()
			select {
			case lease.messages <- payload:
			case <-entry.done:
				return
			}
			entry.pool.mu.Lock()
			lease.pending = false
			entry.pool.mu.Unlock()
		}
		if lease.terminal {
			// Never start another active read after the terminal message. Close
			// must resolve this lease before the receiver can enter the idle/new
			// lease phase, so no previous active read crosses successful reuse.
			select {
			case <-lease.released:
			case <-entry.done:
				return
			}
		}
	}
}

func (entry *providerWebSocket) next(ctx context.Context, lease *providerWebSocketLease) ([]byte, error) {
	// Deliver already received terminal data even if the peer closes immediately.
	select {
	case payload := <-lease.messages:
		return payload, nil
	default:
	}
	select {
	case payload := <-lease.messages:
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-entry.done:
		select {
		case payload := <-lease.messages:
			return payload, nil
		default:
		}
		entry.pool.mu.Lock()
		err := entry.err
		entry.pool.mu.Unlock()
		return nil, err
	}
}

func (entry *providerWebSocket) release(lease *providerWebSocketLease, reuse bool) bool {
	p := entry.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, present := p.entries[entry]; !present || entry.lease != lease {
		return false
	}
	if !reuse || !lease.terminal || lease.pending || len(lease.messages) != 0 || time.Since(entry.born) >= providerWebSocketLifetime {
		p.discardLocked(entry, errors.New("websocket response closed"))
		return false
	}
	entry.busy = false
	entry.lease = nil
	close(lease.released)
	entry.idleTimer = time.AfterFunc(providerWebSocketIdle, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !entry.busy {
			p.discardLocked(entry, errors.New("idle websocket expired"))
		}
	})
	p.signalLocked()
	return true
}

// Source: codex-rs/core/src/client.rs:824:840,1237:1262,1789:1796 and
// codex-rs/core/src/responses_metadata.rs:307:353 in the read-only Codex clone.
// Turn metadata travels in response.create, not a sticky handshake header.
func webSocketRequest(body []byte, headers http.Header, cacheKey string) ([]byte, http.Header, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, nil, false, errors.New("invalid provider request")
	}
	var stream bool
	if value := fields["stream"]; value != nil {
		if err := json.Unmarshal(value, &stream); err != nil {
			return nil, nil, false, err
		}
	}
	delete(fields, "stream")
	// Full-input mode never relies on a previous connection's response cache.
	if value := fields["previous_response_id"]; len(value) != 0 && string(value) != "null" {
		return nil, nil, false, errors.New("provider websocket full-input mode does not accept previous_response_id")
	}
	delete(fields, "previous_response_id")
	fields["type"] = json.RawMessage(`"response.create"`)
	metadata := make(map[string]json.RawMessage)
	if value := fields["client_metadata"]; len(value) != 0 && string(value) != "null" {
		if err := json.Unmarshal(value, &metadata); err != nil || metadata == nil {
			return nil, nil, false, errors.New("invalid provider client_metadata")
		}
	}
	handshake := headers.Clone()
	for _, key := range []string{codexTurnMetadataHeader, "x-codex-turn-state"} {
		if value := headers.Get(key); value != "" {
			if _, present := metadata[key]; !present || key == "x-codex-turn-state" {
				metadata[key], _ = json.Marshal(value)
			}
		}
		handshake.Del(key)
	}
	for _, key := range []string{threadIDHeader, codexWindowIDHeader, openAISubagentHeader} {
		if value := headers.Get(key); value != "" {
			if _, exists := metadata[key]; !exists {
				metadata[key], _ = json.Marshal(value)
			}
		}
	}
	if validCodexCacheKey(cacheKey) {
		handshake[codexSessionIDHeader] = []string{cacheKey}
	}
	// Match Codex's stable handshake identity; request correlation stays in the
	// capturer's context and does not force a new connection on every turn.
	handshake.Del(clientRequestIDHeader)
	if thread := headers.Get(threadIDHeader); thread != "" {
		handshake.Set(clientRequestIDHeader, thread)
	}
	handshake.Del(mekugiCaptureIDHeader)
	handshake.Del("Content-Type")
	handshake.Del("Accept")
	handshake.Set("OpenAI-Beta", responsesWebSocketBeta)
	if value := headers.Get(codexResponsesLiteHeader); value != "" {
		metadata["ws_request_header_x_openai_internal_codex_responses_lite"], _ = json.Marshal(value)
	}
	if len(metadata) != 0 {
		fields["client_metadata"], _ = json.Marshal(metadata)
	}
	payload, err := json.Marshal(fields)
	return payload, handshake, stream, err
}

func (c *providerClient) forwardWebSocket(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, bool, error) {
	payload, handshake, stream, err := webSocketRequest(body, headers, cacheKey)
	if err != nil {
		return nil, false, err
	}
	requestCtx, cancel := context.WithCancel(responseCtx)
	stopStart := context.AfterFunc(startCtx, cancel)
	entry, responseHeaders, rejected, err := c.websockets.acquire(requestCtx, handshake, func(responseHeaders http.Header) *capturer.WebSocketAttempt {
		return capturer.BeginWebSocketAttempt(responseCtx, payload, headers, responseHeaders)
	})
	if err != nil {
		if !stopStart() {
			if rejected != nil {
				_ = rejected.Body.Close()
			}
			cancel()
			return nil, false, fmt.Errorf("connect provider websocket: %w", context.Canceled)
		}
		if rejected != nil {
			switch rejected.StatusCode {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
				_ = rejected.Body.Close()
				cancel()
				return nil, true, nil // Explicit unsupported handshake; no response.create was sent.
			}
			if rejected.StatusCode != http.StatusSwitchingProtocols {
				var responseBody io.ReadCloser = &cancelOnCloseReadCloser{body: rejected.Body, cancel: cancel}
				if c.streamIdleTimeout > 0 {
					responseBody = newStreamIdleReadCloser(responseCtx, responseBody, c.streamIdleTimeout)
				}
				observation := capturer.BeginWebSocketAttempt(responseCtx, nil, headers, rejected.Header)
				rejected.Body = observation.HTTPErrorBody(responseBody, rejected.StatusCode, rejected.Header.Get("Content-Type"), rejected.Header.Get("Content-Encoding"))
				return rejected, false, nil
			}
			_ = rejected.Body.Close()
		}
		cancel()
		return nil, false, fmt.Errorf("connect provider websocket: %w", err)
	}
	lease := entry.lease
	observation := lease.observation
	fail := func(err error) (*http.Response, bool, error) {
		stopStart()
		cancel()
		entry.release(lease, false)
		<-entry.receiverDone
		observation.Finish(err)
		return nil, false, fmt.Errorf("forward provider websocket: %w", err)
	}
	// A failed Write may have sent the complete message. Never resend it or
	// switch to HTTP once writing response.create has begun.
	if err := entry.conn.Write(requestCtx, websocket.MessageText, payload); err != nil {
		return fail(err)
	}
	var prefetched [][]byte
	var event struct {
		Type       string                     `json:"type"`
		Status     int                        `json:"status"`
		StatusCode int                        `json:"status_code"`
		Headers    map[string]json.RawMessage `json:"headers"`
	}
	prefixBytes := 0
	for {
		if err := startCtx.Err(); err != nil {
			return fail(err)
		}
		if err := requestCtx.Err(); err != nil {
			return fail(err)
		}
		message, err := entry.next(requestCtx, lease)
		if err != nil {
			return fail(err)
		}
		// Decode into a fresh envelope; absent fields must not retain earlier
		// ancillary metadata while deciding the actual response status.
		event.Type, event.Status, event.StatusCode, event.Headers = "", 0, 0, nil
		if json.Unmarshal(message, &event) != nil {
			return fail(errors.New("provider websocket sent invalid JSON"))
		}
		if !isResponseAncillaryEvent(event.Type) {
			if event.Type == "error" {
				prefetched = nil
			}
			prefetched = append(prefetched, message)
			break
		}
		prefixBytes += len(message)
		if prefixBytes > maxUpstreamSniffBytes {
			return fail(errors.New("provider websocket ancillary prefix exceeds the router inspection budget"))
		}
		prefetched = append(prefetched, message)
	}
	if !stopStart() {
		err := startCtx.Err()
		if err == nil {
			err = context.Canceled
		}
		return fail(err)
	}
	result := &webSocketResponseBody{entry: entry, lease: lease, ctx: requestCtx, cancel: cancel, observation: observation, stream: stream, idleTimeout: c.streamIdleTimeout, prefetched: prefetched}
	result.stopCancellation = context.AfterFunc(requestCtx, func() { entry.release(lease, false) })
	status := http.StatusOK
	if event.Type == "error" {
		status = event.Status
		if status == 0 {
			status = event.StatusCode
		}
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		result.stream = false
		result.errorResponse = true
	}
	visibleHeaders := responseHeaders.Clone()
	if visibleHeaders == nil {
		visibleHeaders = make(http.Header)
	}
	if event.Type == "error" {
		for name, raw := range event.Headers {
			value := string(raw)
			var decoded string
			if json.Unmarshal(raw, &decoded) == nil {
				value = decoded
			} else if value != "true" && value != "false" && (len(value) == 0 || (value[0] != '-' && (value[0] < '0' || value[0] > '9'))) {
				continue
			}
			if strings.ContainsAny(name+value, "\r\n") {
				continue
			}
			parsed, err := textproto.NewReader(bufio.NewReader(strings.NewReader(name + ": " + value + "\r\n\r\n"))).ReadMIMEHeader()
			if err == nil {
				maps.Copy(visibleHeaders, parsed)
			}
		}
	}
	for _, key := range []string{"Connection", "Upgrade", "Sec-WebSocket-Accept", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol", "Content-Length", "Content-Encoding"} {
		visibleHeaders.Del(key)
	}
	if result.stream {
		visibleHeaders.Set("Content-Type", "text/event-stream")
	} else {
		visibleHeaders.Set("Content-Type", "application/json")
	}
	return &http.Response{StatusCode: status, Header: visibleHeaders, Body: result}, false, nil
}

type webSocketResponseBody struct {
	mu               sync.Mutex
	closing          atomic.Bool
	items            map[int]json.RawMessage
	itemBytes        int
	entry            *providerWebSocket
	lease            *providerWebSocketLease
	ctx              context.Context
	cancel           context.CancelFunc
	stopCancellation func() bool
	observation      *capturer.WebSocketAttempt
	stream           bool
	errorResponse    bool
	idleTimeout      time.Duration
	prefetched       [][]byte
	buffer           bytes.Buffer
	terminal         bool
	closed           bool
	readErr          error
}

func (body *webSocketResponseBody) Read(destination []byte) (int, error) {
	body.mu.Lock()
	defer body.mu.Unlock()
	for body.buffer.Len() == 0 {
		if body.closing.Load() || body.closed {
			return 0, io.ErrClosedPipe
		}
		if body.terminal {
			return 0, io.EOF
		}
		var payload []byte
		if len(body.prefetched) != 0 {
			payload = body.prefetched[0]
			body.prefetched[0] = nil
			body.prefetched = body.prefetched[1:]
		}
		if payload == nil {
			ctx := body.ctx
			cancel := func() {}
			if body.idleTimeout > 0 {
				ctx, cancel = context.WithTimeout(body.ctx, body.idleTimeout)
			}
			var err error
			payload, err = body.entry.next(ctx, body.lease)
			if err != nil && body.ctx.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = errors.Join(errUpstreamStreamIdleTimeout, err)
			}
			cancel()
			if err != nil {
				body.readErr = err
				body.entry.release(body.lease, false)
				return 0, err
			}
		}
		var event struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			Item        json.RawMessage `json:"item"`
			OutputIndex *int            `json:"output_index"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			body.readErr = err
			return 0, err
		}
		if !body.stream && event.Type == "response.output_item.done" && event.OutputIndex != nil && *event.OutputIndex >= 0 {
			// Missing/null items cannot account for a map entry against the byte
			// budget and are not finalized output items in the Responses protocol.
			if len(event.Item) == 0 || bytes.Equal(bytes.TrimSpace(event.Item), []byte("null")) {
				body.readErr = errors.New("missing finalized websocket output item")
				return 0, body.readErr
			}
			if body.items == nil {
				body.items = make(map[int]json.RawMessage)
			}
			body.itemBytes += len(event.Item) - len(body.items[*event.OutputIndex])
			if body.itemBytes > upstreamJSONBufferBytes {
				body.readErr = errors.New("upstream JSON output exceeds the router buffer budget")
				return 0, body.readErr
			}
			body.items[*event.OutputIndex] = event.Item
		}
		var terminal map[string]json.RawMessage
		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete":
			if json.Unmarshal(event.Response, &terminal) != nil || terminal == nil {
				body.readErr = errors.New("invalid websocket terminal response")
				return 0, body.readErr
			}
			// Streaming completion belongs to the event, just as on upstream
			// SSE. A nonstream client needs that state in its unwrapped JSON.
			if !body.stream && jsonString(terminal, "status") == "" {
				terminal["status"], _ = json.Marshal(strings.TrimPrefix(event.Type, "response."))
				event.Response, _ = json.Marshal(terminal)
			}
			body.terminal = true
		case "error":
			body.terminal = true
			body.readErr = errProviderWebSocketResponse
		}
		if body.stream {
			// JSON messages may contain formatting newlines; each SSE data line needs
			// its own prefix so the existing event parser receives exactly the JSON.
			for line := range bytes.SplitSeq(payload, []byte{'\n'}) {
				body.buffer.WriteString("data: ")
				body.buffer.Write(line)
				body.buffer.WriteByte('\n')
			}
			body.buffer.WriteByte('\n')
		} else if body.errorResponse {
			body.buffer.Write(payload)
		} else if body.terminal {
			if event.Type == "error" {
				return 0, body.readErr
			}
			if len(body.items) > 0 {
				var output []json.RawMessage
				if json.Unmarshal(terminal["output"], &output) != nil || len(output) == 0 {
					for _, index := range slices.Sorted(maps.Keys(body.items)) {
						output = append(output, body.items[index])
					}
					terminal["output"], _ = json.Marshal(output)
					event.Response, _ = json.Marshal(terminal)
					if len(event.Response) > upstreamJSONBufferBytes {
						body.readErr = errors.New("upstream JSON response exceeds the router buffer budget")
						return 0, body.readErr
					}
				}
			}
			body.items = nil
			body.buffer.Write(event.Response)
		}
	}
	return body.buffer.Read(destination)
}

func (body *webSocketResponseBody) Close() error {
	if body.closing.Swap(true) {
		return nil
	}
	// Unblock a concurrent Read before serializing observation/finalization.
	// Avoid canceling a completed lease before deciding whether to reuse it.
	if !body.mu.TryLock() {
		body.cancel()
		body.mu.Lock()
	}
	defer body.mu.Unlock()
	if body.closed {
		return nil
	}
	body.closed = true
	body.stopCancellation()
	complete := body.terminal && body.buffer.Len() == 0 && body.readErr == nil && body.ctx.Err() == nil
	if !body.entry.release(body.lease, complete) {
		// The receiver observes successful reads before delivering them. On
		// early close/cancel it must finish that observation before we finalize
		// capture, including a queued message and a blocked delivery reservation.
		<-body.entry.receiverDone
	}
	if !complete && body.readErr == nil {
		body.readErr = io.ErrUnexpectedEOF
	}
	captureErr := body.readErr
	if errors.Is(captureErr, errProviderWebSocketResponse) && body.terminal && body.buffer.Len() == 0 {
		captureErr = nil
	}
	body.observation.Finish(captureErr)
	body.cancel()
	body.prefetched = nil
	body.buffer.Reset()
	return nil
}

// Preserve non-upgrade HTTP bodies before the WebSocket library's bounded
// diagnostic read. Successful upgrades retain their io.ReadWriteCloser intact.
type webSocketHandshakeTransport struct {
	http.RoundTripper
	rejected *http.Response
}

func (transport *webSocketHandshakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil || response.StatusCode == http.StatusSwitchingProtocols {
		return response, err
	}
	transport.rejected = response
	headerOnly := *response
	headerOnly.Body = http.NoBody
	return &headerOnly, nil
}
