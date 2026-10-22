// Source: capturer/websocket.go@b5162407.
package capture

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"iter"
	"net/http"
	"sync"
	"time"
)

const webSocketContentType = "application/x-openai-websocket-json"

type ResponsesWebSocketControlBoundary string

const (
	// ResponsesWebSocketControlCodex is the client-facing transport boundary.
	ResponsesWebSocketControlCodex ResponsesWebSocketControlBoundary = "codex"
	// ResponsesWebSocketControlProvider is the provider-facing transport boundary.
	ResponsesWebSocketControlProvider ResponsesWebSocketControlBoundary = "provider"
)

type ResponsesWebSocketControlDirection string

const (
	// ResponsesWebSocketControlRequest identifies a control message sent
	// toward a transport boundary.
	ResponsesWebSocketControlRequest ResponsesWebSocketControlDirection = "request"
	// ResponsesWebSocketControlResponse identifies a control message returned
	// from a transport boundary.
	ResponsesWebSocketControlResponse ResponsesWebSocketControlDirection = "response"
)

type responsesWebSocketFactoryKey struct{}

type responsesWebSocketCapture struct {
	recorder  *Recorder
	headers   http.Header
	rejection *requestState

	controlCaptureID string
}

// BeginWebSocketRejection observes a terminal provider upgrade rejection before
// response.create. Negotiation fallback and successful upgrades create no request.
// Recorder.Handler records the actual downstream HTTP response on return.
func BeginWebSocketRejection(ctx context.Context, headers, responseHeaders http.Header, status int) *WebSocketAttempt {
	capture, _ := ctx.Value(responsesWebSocketFactoryKey{}).(*responsesWebSocketCapture)
	if capture == nil || status < http.StatusBadRequest || status == http.StatusUpgradeRequired {
		return nil
	}
	state := capture.recorder.beginRequest(capture.headers)
	capture.rejection = state
	ctx = context.WithValue(ctx, captureKey{}, state)
	// No inference body exists at this boundary. Record its known absence.
	ObserveProjectedRequest(ctx, nil)
	return BeginWebSocketAttempt(ctx, nil, headers, responseHeaders)
}

// ResponsesWebSocket observes one logical response delivered over the
// Codex-facing WebSocket. Its messages are the restored JSON payloads delivered
// to Codex, without WebSocket framing or provider-only representation.
type ResponsesWebSocket struct {
	state    *requestState
	started  time.Time
	request  []byte
	response boundedObservation

	mu       sync.Mutex
	finished bool
}

// BeginResponsesWebSocket starts one logical Codex-facing exchange when ctx
// came from Recorder.Handler's GET /v1/responses handler. A nil payload is a
// valid automatic successor and is measured as zero request bytes.
func BeginResponsesWebSocket(ctx context.Context, headers http.Header, payload []byte) (context.Context, *ResponsesWebSocket) {
	capture, _ := ctx.Value(responsesWebSocketFactoryKey{}).(*responsesWebSocketCapture)
	if capture == nil {
		return ctx, nil
	}
	return capture.begin(ctx, headers, payload)
}

func (capture *responsesWebSocketCapture) begin(ctx context.Context, headers http.Header, payload []byte) (context.Context, *ResponsesWebSocket) {
	state := capture.recorder.beginRequest(headers)
	scoped := context.WithValue(ctx, captureKey{}, state)
	return scoped, &ResponsesWebSocket{
		state:   state,
		started: time.Now(),
		request: bytes.Clone(payload),
	}
}
func (r *Recorder) newResponsesWebSocketCapture(headers http.Header) *responsesWebSocketCapture {
	return &responsesWebSocketCapture{
		recorder:         r,
		headers:          headers.Clone(),
		controlCaptureID: randomCaptureID(),
	}
}

// ObserveResponsesWebSocketControl records one actual WebSocket control
// payload at either transport boundary. Call it once for the request payload
// sent toward a boundary and once for the response payload read from it.
func ObserveResponsesWebSocketControl(ctx context.Context, boundary ResponsesWebSocketControlBoundary, direction ResponsesWebSocketControlDirection, payload []byte) {
	capture, _ := ctx.Value(responsesWebSocketFactoryKey{}).(*responsesWebSocketCapture)
	if capture == nil {
		return
	}
	capture.observeControl(boundary, direction, payload)
}

func (capture *responsesWebSocketCapture) observeControl(boundary ResponsesWebSocketControlBoundary, direction ResponsesWebSocketControlDirection, payload []byte) {
	if boundary != ResponsesWebSocketControlCodex && boundary != ResponsesWebSocketControlProvider {
		return
	}
	if direction != ResponsesWebSocketControlRequest && direction != ResponsesWebSocketControlResponse {
		return
	}
	measurement := payloadMetrics{Bytes: uint64(len(payload))}
	record := captureRecord{
		SchemaVersion:    schemaVersion,
		Boundary:         string(boundary) + "_control",
		ControlDirection: direction,
		CaptureID:        capture.controlCaptureID,
		Mode:             capture.recorder.mode,
		RequestID:        safeDiagnosticIdentifier(capture.headers.Get("x-client-request-id")),
		SessionID:        safeDiagnosticIdentifier(capture.headers.Get("session-id")),
		ThreadID:         safeDiagnosticIdentifier(capture.headers.Get("thread-id")),
		Subagent:         safeDiagnosticIdentifier(capture.headers.Get("x-openai-subagent")),
		StatusCode:       http.StatusSwitchingProtocols,
		ResponseComplete: true,
		ResponseStatus:   "control",
		Transport:        "websocket",
		CapturedAt:       time.Now().UTC(),
	}
	if direction == ResponsesWebSocketControlRequest {
		record.Request = measurement
	} else {
		record.Response = measurement
	}
	capture.recorder.write(record, nil)
}

// Message measures one restored JSON message delivered to Codex.
func (exchange *ResponsesWebSocket) Message(payload []byte) {
	if exchange == nil {
		return
	}
	exchange.mu.Lock()
	defer exchange.mu.Unlock()
	if exchange.finished {
		return
	}
	_, _ = exchange.response.Write(payload)
}

// Finish records the logical exchange after its terminal downstream message and
// correlated provider attempts have been observed.
func (exchange *ResponsesWebSocket) Finish(err error) {
	if exchange == nil {
		return
	}
	exchange.mu.Lock()
	if exchange.finished {
		exchange.mu.Unlock()
		return
	}
	exchange.finished = true
	request := exchange.request
	response := exchange.response.snapshot()
	exchange.request = nil
	exchange.response = boundedObservation{}
	exchange.mu.Unlock()

	exchange.state.recorder.recordExchange(exchange.state, "codex", 0, exchange.started, request, response, http.StatusSwitchingProtocols, webSocketContentType, "", err, providerResponseEvidence{})
}

// WebSocketAttempt observes one response.create exchange on a possibly reused
// connection. Message records contain actual JSON payload bytes, not WebSocket
// frame headers, control frames, or the router's downstream SSE serialization.
// Like Transport, this is a transport-boundary observer, not a metrics callback.
// Its owner calls Finish after terminal usage observation and body consumption.
type WebSocketAttempt struct {
	state    *requestState
	attempt  uint64
	started  time.Time
	request  []byte
	response boundedObservation
	evidence providerResponseEvidence
	finished bool
}

// BeginWebSocketAttempt is a no-op outside a Recorder handler. Headers describe
// this exchange's routing, not a prior exchange's connection handshake.
func BeginWebSocketAttempt(ctx context.Context, payload []byte, headers, responseHeaders http.Header) *WebSocketAttempt {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return nil
	}
	attempt := state.beginProviderAttempt()
	var request struct {
		Metadata map[string]jsonv1.RawMessage `json:"client_metadata"`
	}
	var turnState string
	if json.Unmarshal(payload, &request, jsonv1.DefaultOptionsV1()) == nil && json.Unmarshal(request.Metadata["x-codex-turn-state"], &turnState, jsonv1.DefaultOptionsV1()) == nil {
		headers = headers.Clone()
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("x-codex-turn-state", turnState)
	}
	state.observeProviderRouting(attempt, headers)
	return &WebSocketAttempt{state: state, attempt: attempt, started: time.Now(), request: bytes.Clone(payload), evidence: providerHeaderEvidence(responseHeaders)}
}

func (state *requestState) observeProviderRouting(attempt uint64, headers http.Header) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.providerRouting == nil {
		state.providerRouting = make(map[uint64]requestRouting)
	}
	routing := requestRouting{turnState: state.recorder.turnStateFingerprint(headers.Get("x-codex-turn-state"))}
	if key := headers.Get("Session_id"); key != "" {
		routing.sessionKey = state.recorder.fingerprint("cache-key", key)
	}
	state.providerRouting[attempt] = routing
}

// Message measures the bytes read from one provider text message. Observation
// remains bounded even when the complete exchange exceeds the parsing budget.
func (attempt *WebSocketAttempt) Message(payload []byte) {
	if attempt == nil || attempt.finished {
		return
	}
	_, _ = attempt.response.Write(payload)
}

func (attempt *WebSocketAttempt) Finish(err error) {
	if attempt == nil || attempt.finished {
		return
	}
	attempt.finished = true
	attempt.state.recorder.recordExchange(attempt.state, "provider", attempt.attempt, attempt.started, attempt.request, attempt.response.snapshot(), http.StatusSwitchingProtocols, webSocketContentType, "", err, attempt.evidence)
	attempt.request = nil
	attempt.response = boundedObservation{}
}

// HTTPErrorBody observes a rejected upgrade's actual HTTP body. No inference
// payload was sent, so its request measurement is empty rather than fabricated.
func (attempt *WebSocketAttempt) HTTPErrorBody(body io.ReadCloser, status int, contentType, contentEncoding string) io.ReadCloser {
	if attempt == nil {
		return body
	}
	return &observedResponseBody{ReadCloser: body, finish: func(response observedPayload, err error) {
		if attempt.finished {
			return
		}
		attempt.finished = true
		attempt.state.recorder.recordExchange(attempt.state, "provider", attempt.attempt, attempt.started, nil, response, status, contentType, contentEncoding, err, attempt.evidence)
	}}
}

func webSocketMessages(payload []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		decoder := jsontext.NewDecoder(bytes.NewReader(payload))
		for {
			var message jsonv1.RawMessage
			if err := json.UnmarshalDecode(decoder, &message, jsonv1.DefaultOptionsV1()); err != nil {
				return
			}
			if !yield(message) {
				return
			}
		}
	}
}
