// Source: capturer/capturer.go@b5162407.
// Package capture observes the router's existing client and provider seams
// without adding a listener or changing request and response bytes.
package capture

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/httptransport"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/libraries/persistence"
)

const schemaVersion = 7

// Detailed exchanges are diagnostic evidence rather than the cumulative
// counters. Keeping a fixed recent window prevents an always-on router from
// retaining request history without bound; overflow is explicit capture
// health so incomplete detail cannot be mistaken for complete evidence.
const maxRetainedExchangeDetails = 4096

const maxObservedResponseBytes = 8 << 20

type captureKey struct{}

// Config identifies the observed router behavior and optional durable JSONL
// destination. An empty Output retains records only for the process lifetime.
type Config struct {
	Writes *persistence.Counter
	Output string
	Mode   string
}

// ProviderUsage is the provider-authoritative token count parsed from one
// terminal Responses payload.
type ProviderUsage struct {
	// Nil means legacy normalization did not retain field-completeness evidence.
	EvidenceComplete *bool                      `json:"evidence_complete,omitempty"`
	InputTokens      uint64                     `json:"input_tokens"`
	CachedTokens     uint64                     `json:"cached_input_tokens"`
	OutputTokens     uint64                     `json:"output_tokens"`
	ReasoningTokens  uint64                     `json:"reasoning_tokens"`
	OutputThroughput inference.OutputThroughput `json:"output_throughput,omitzero"`
}

type payloadMetrics struct {
	Bytes uint64 `json:"bytes"`
}

type captureRecord struct {
	Transport            string                             `json:"transport,omitempty"`
	ControlDirection     ResponsesWebSocketControlDirection `json:"control_direction,omitempty"`
	ProviderResponse     *providerResponseEvidence          `json:"provider_response,omitempty"`
	PredecessorSequence  uint64                             `json:"predecessor_sequence,omitempty"`
	SchemaVersion        int                                `json:"schema_version"`
	Boundary             string                             `json:"boundary"`
	CaptureID            string                             `json:"capture_id"`
	RequestSequence      uint64                             `json:"request_sequence"`
	ProviderAttempt      uint64                             `json:"provider_attempt,omitempty"`
	Mode                 string                             `json:"mode"`
	RequestID            string                             `json:"request_id,omitempty"`
	SessionID            string                             `json:"session_id,omitempty"`
	ThreadID             string                             `json:"thread_id,omitempty"`
	Subagent             string                             `json:"subagent,omitempty"`
	RequestKind          string                             `json:"request_kind,omitempty"`
	ProviderExpected     *bool                              `json:"provider_expected,omitempty"`
	RequestModel         string                             `json:"request_model,omitempty"`
	Request              payloadMetrics                     `json:"request"`
	Fingerprint          *requestFingerprint                `json:"cache_fingerprint,omitempty"`
	ProjectedFingerprint *requestFingerprint                `json:"projected_fingerprint,omitempty"`
	RequestTools         []string                           `json:"request_tools,omitempty"`
	StatusCode           int                                `json:"status_code"`
	ResponseComplete     bool                               `json:"response_complete"`
	ResponseStatus       string                             `json:"response_status,omitempty"`
	Usage                *ProviderUsage                     `json:"usage,omitempty"`
	Response             payloadMetrics                     `json:"response"`
	CaptureError         string                             `json:"capture_error,omitempty"`
	DurationMillis       uint64                             `json:"duration_ms"`
	CapturedAt           time.Time                          `json:"captured_at"`
}

// Recorder owns correlation, sanitized measurement, durable capture, and
// derived metrics for one router process.
type Recorder struct {
	writes              *persistence.Counter
	lastRequestSequence map[string]uint64
	fingerprintKey      [32]byte
	mu                  sync.Mutex
	file                *os.File
	mode                string
	requestSequence     uint64
	metrics             MetricsSnapshot
}

type requestState struct {
	requestKind          string
	providerNotExpected  bool
	predecessorSequence  uint64
	recorder             *Recorder
	projectedObserved    bool
	projectedFingerprint *requestFingerprint
	providerRouting      map[uint64]requestRouting
	clientTurnState      string
	mu                   sync.Mutex
	captureID            string
	sequence             uint64
	providerAttempts     uint64
	requestID            string
	sessionID            string
	threadID             string
	subagent             string
	providers            []captureRecord
	providerUsage        map[uint64]ProviderUsage
}

type requestRouting struct {
	sessionKey string
	turnState  string
}

// ObserveLocalResponse records the router's decision to answer without a
// provider invocation. Actual provider attempts remain independently captured.
func ObserveLocalResponse(ctx context.Context) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	state.providerNotExpected = true
	state.mu.Unlock()
}

// ObserveRequestKind records only the router's validated, content-free request kind.
func ObserveRequestKind(ctx context.Context, kind string) {
	if !responseevents.RequestKind(kind).Known() {
		return
	}
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	state.requestKind = kind
	state.mu.Unlock()
}

// ObserveProviderUsage supplies the provider-authoritative usage parsed by the
// router for the current provider attempt. Requests outside a Recorder handler
// are ignored.
func ObserveProviderUsage(ctx context.Context, usage ProviderUsage) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.providerAttempts == 0 {
		return
	}
	if state.providerUsage == nil {
		state.providerUsage = make(map[uint64]ProviderUsage)
	}
	state.providerUsage[state.providerAttempts] = usage
}

// ObserveProjectedRequest supplies the actual request after replay and tool
// projection. Only a privacy-safe fingerprint is retained, not the raw body.
// It is observation only and cannot fail or change the routed operation.
func ObserveProjectedRequest(ctx context.Context, body []byte) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	fingerprint := state.recorder.requestFingerprint(body)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.projectedObserved = true
	state.projectedFingerprint = fingerprint
}

// Open creates one recorder whose caller must close it before releasing its
// invocation outputs. Composing the service itself acquires no resources.
func (*Service) Open(config Config) (*Recorder, error) {
	var file *os.File
	var err error
	if config.Output != "" {
		file, err = os.OpenFile(config.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open capture output: %w", err)
		}
	}
	var fingerprintKey [32]byte
	rand.Read(fingerprintKey[:])
	return &Recorder{
		writes: config.Writes, fingerprintKey: fingerprintKey, file: file, mode: config.Mode,
		lastRequestSequence: make(map[string]uint64),
		metrics:             newMetricsSnapshot(config.Mode),
	}, nil
}

// Close flushes the optional durable destination.
func (r *Recorder) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	return r.file.Close()
}

// Handler observes the existing Responses listener. POST requests are measured
// directly. GET requests receive a private WebSocket capture factory in their
// context, but the upgrade itself does not create a logical request.
func (r *Recorder) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/responses" && request.Method == http.MethodGet {
			capture := r.newResponsesWebSocketCapture(request.Header)
			request = request.WithContext(context.WithValue(request.Context(), responsesWebSocketFactoryKey{}, capture))
			response := &observedResponseWriter{ResponseWriter: writer}
			started := time.Now()
			defer func() {
				if capture.rejection != nil {
					r.recordExchange(capture.rejection, "codex", 0, started, nil,
						response.content.snapshot(), response.statusCode(), response.Header().Get("Content-Type"),
						response.Header().Get("Content-Encoding"), response.writeError, providerResponseEvidence{})
				}
			}()
			next.ServeHTTP(response, request)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			next.ServeHTTP(writer, request)
			return
		}
		state := r.beginRequest(request.Header)
		started := time.Now()
		requestBody := &observedReadCloser{ReadCloser: request.Body}
		request.Body = requestBody
		response := &observedResponseWriter{ResponseWriter: writer}
		request = request.WithContext(context.WithValue(request.Context(), captureKey{}, state))
		defer func() {
			r.recordExchange(
				state, "codex", 0, started,
				requestBody.content.Bytes(), response.content.snapshot(), response.statusCode(),
				response.Header().Get("Content-Type"), response.Header().Get("Content-Encoding"), errors.Join(requestBody.readError, response.writeError), providerResponseEvidence{},
			)
		}()
		next.ServeHTTP(response, request)
	})
}

// Transport observes provider attempts made by the router's existing HTTP
// client. It preserves retry, cancellation, and response-body ownership.
func (r *Recorder) Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		state, ok := request.Context().Value(captureKey{}).(*requestState)
		if !ok || request.Method != http.MethodPost || !(strings.HasSuffix(request.URL.Path, "/responses") || strings.HasSuffix(request.URL.Path, "/chat/completions") || strings.HasSuffix(request.URL.Path, "/messages")) {
			return next.RoundTrip(request)
		}
		attempt := state.beginProviderAttempt()
		requestBody, borrowed := httptransport.RequestBytes(request)
		if !borrowed {
			var err error
			requestBody, err = snapshotRequestBody(request)
			if err != nil {
				return nil, fmt.Errorf("capture provider request body: %w", err)
			}
		}
		// Materialize only sanitized facts before forwarding. Borrowed bytes need
		// not remain immutable after the transport releases its request body.
		observation := r.observeRequest(requestBody)
		state.observeProviderRouting(attempt, request.Header)
		started := time.Now()
		response, roundTripErr := next.RoundTrip(request)
		if roundTripErr != nil {
			r.recordObservedExchange(state, "provider", attempt, started, time.Now(), observation, observedPayload{}, 0, "", "", roundTripErr, providerResponseEvidence{CachedTokensState: "unavailable"})
			return nil, roundTripErr
		}
		contentType := response.Header.Get("Content-Type")
		contentEncoding := response.Header.Get("Content-Encoding")
		statusCode := response.StatusCode
		evidence := providerHeaderEvidence(response.Header)
		if strings.HasSuffix(request.URL.Path, "/messages") {
			evidence.wireFormat = "anthropic"
		}
		response.Body = &observedResponseBody{
			ReadCloser: response.Body,
			finish: func(body observedPayload, readErr error) {
				r.recordObservedExchange(state, "provider", attempt, started, time.Now(), observation, body, statusCode, contentType, contentEncoding, readErr, evidence)
			},
		}
		return response, nil
	})
}

func (r *Recorder) beginRequest(header http.Header) *requestState {
	state := &requestState{
		captureID:       randomCaptureID(),
		recorder:        r,
		clientTurnState: r.turnStateFingerprint(header.Get("x-codex-turn-state")),
		requestID:       safeDiagnosticIdentifier(header.Get("x-client-request-id")),
		sessionID:       safeDiagnosticIdentifier(cmp.Or(header.Get("session-id"), header.Get("Session_id"))),
		threadID:        safeDiagnosticIdentifier(header.Get("thread-id")),
		subagent:        safeDiagnosticIdentifier(header.Get("x-openai-subagent")),
	}
	r.mu.Lock()
	r.requestSequence++
	state.sequence = r.requestSequence
	if state.threadID != "" {
		// Evicted arrival metadata yields an unavailable comparison, never a
		// comparison against an older surviving request from that thread.
		if _, known := r.lastRequestSequence[state.threadID]; !known && len(r.lastRequestSequence) >= maxRetainedExchangeDetails {
			var oldestThread string
			oldest := ^uint64(0)
			for thread, sequence := range r.lastRequestSequence {
				if sequence < oldest {
					oldestThread, oldest = thread, sequence
				}
			}
			delete(r.lastRequestSequence, oldestThread)
		}
		state.predecessorSequence = r.lastRequestSequence[state.threadID]
		r.lastRequestSequence[state.threadID] = state.sequence
	}
	r.mu.Unlock()
	return state
}

func (s *requestState) beginProviderAttempt() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerAttempts++
	return s.providerAttempts
}

func (s *requestState) addProvider(record captureRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers = append(s.providers, record)
}

func (s *requestState) providerRecords() []captureRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.providers)
}

// observedUsage retrieves the provider usage observation for a specific attempt.
func (s *requestState) observedUsage(boundary string, attempt uint64) *ProviderUsage {
	if boundary != "provider" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	usage, ok := s.providerUsage[attempt]
	if !ok {
		return nil
	}
	return new(usage)
}

type requestObservation struct {
	bytes       uint64
	fingerprint *requestFingerprint
	model       string
	tools       []string
	noGenerate  bool
	chat        bool
	err         string
}

func (r *Recorder) observeRequest(body []byte) requestObservation {
	observation := requestObservation{bytes: uint64(len(body)), fingerprint: r.requestFingerprint(body)}
	var envelope struct {
		Generate *bool               `json:"generate"`
		Model    string              `json:"model"`
		Messages jsonv1.RawMessage   `json:"messages"`
		Tools    []jsonv1.RawMessage `json:"tools"`
	}
	if len(body) != 0 {
		if err := json.Unmarshal(body, &envelope, jsonv1.DefaultOptionsV1()); err != nil {
			observation.err = "invalid request JSON"
		} else {
			observation.model = safeDiagnosticIdentifier(envelope.Model)
			observation.noGenerate = envelope.Generate != nil && !*envelope.Generate
			observation.tools = requestToolNames(envelope.Tools)
			if strings.TrimSpace(envelope.Model) == "" {
				observation.err = "missing request model"
			}
		}
	}
	observation.chat = len(envelope.Messages) > 0
	return observation
}

func (r *Recorder) recordExchange(state *requestState, boundary string, attempt uint64, started time.Time, requestBody []byte, responseBody observedPayload, statusCode int, contentType, contentEncoding string, exchangeErr error, evidence providerResponseEvidence) {
	// Completion precedes request decoding, including on raw HTTP/WebSocket
	// paths that have not already materialized a detached observation.
	completed := time.Now()
	r.recordObservedExchange(state, boundary, attempt, started, completed, r.observeRequest(requestBody), responseBody, statusCode, contentType, contentEncoding, exchangeErr, evidence)
}

func (r *Recorder) recordObservedExchange(state *requestState, boundary string, attempt uint64, started, completed time.Time, request requestObservation, responseBody observedPayload, statusCode int, contentType, contentEncoding string, exchangeErr error, evidence providerResponseEvidence) {
	record := captureRecord{
		SchemaVersion:   schemaVersion,
		Boundary:        boundary,
		CaptureID:       state.captureID,
		RequestSequence: state.sequence, PredecessorSequence: state.predecessorSequence,
		ProviderAttempt: attempt,
		Mode:            r.mode,
		RequestID:       state.requestID,
		SessionID:       state.sessionID,
		ThreadID:        state.threadID,
		Subagent:        state.subagent,
		StatusCode:      statusCode,
		DurationMillis:  uint64(max(completed.Sub(started).Milliseconds(), 0)),
		CapturedAt:      completed.UTC(),
		Usage:           state.observedUsage(boundary, attempt),
	}
	state.mu.Lock()
	record.RequestKind = state.requestKind
	if boundary == "codex" && state.providerNotExpected {
		record.ProviderExpected = new(false)
	}
	state.mu.Unlock()
	if contentType == webSocketContentType {
		record.Transport = "websocket"
	}
	if boundary == "provider" {
		if evidence.CachedTokensState == "" {
			evidence.CachedTokensState = "unavailable"
		}
		record.ProviderResponse = &evidence
	}
	if exchangeErr != nil {
		record.CaptureError = "request or response stream failed"
	}
	record.Request.Bytes = request.bytes
	record.Fingerprint = request.fingerprint
	if boundary == "codex" && record.Fingerprint != nil {
		value := state.clientTurnState
		record.Fingerprint.TurnState = &value
	}
	if boundary == "provider" {
		state.mu.Lock()
		record.ProjectedFingerprint = state.projectedFingerprint
		if record.Fingerprint != nil {
			if routing, ok := state.providerRouting[attempt]; ok {
				record.Fingerprint.RoutingKey = routing.sessionKey
				record.Fingerprint.TurnState = &routing.turnState
			}
		}

		projectedObserved := state.projectedObserved
		state.mu.Unlock()
		if !projectedObserved {
			record.CaptureError = "missing projected request observation"
		}
	}
	record.RequestModel = request.model
	record.RequestTools = request.tools
	if boundary == "codex" && request.noGenerate {
		record.ProviderExpected = new(false)
	}
	if request.err != "" {
		record.CaptureError = request.err
	}
	record.Response.Bytes = responseBody.bytes
	observedContent, err := decodedCapturePayload(responseBody.content, contentEncoding)
	if responseBody.overflow || errors.Is(err, errDecodedPayloadTooLarge) {
		record.CaptureError = "response payload exceeds capture observation limit"
	} else if err != nil {
		record.CaptureError = "unsupported or invalid response content encoding"
	} else if len(observedContent) != 0 {
		lowerContentType := strings.ToLower(contentType)
		if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices ||
			strings.Contains(lowerContentType, "json") || strings.Contains(lowerContentType, "text/event-stream") ||
			capturedPayloadLooksLikeSSE(observedContent) {
			if evidence.wireFormat == "anthropic" {
				observeAnthropicResponse(observedContent, &record)
			} else if request.chat {
				observeChatResponse(observedContent, contentType, &record)
			} else {
				observeResponse(observedContent, contentType, &record)
			}
		}
	}
	switch record.ResponseStatus {
	case "completed", "failed", "incomplete", "cancelled", "error":
		record.ResponseComplete = true
	}
	if (statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices) && contentType != webSocketContentType {
		record.ResponseComplete = true
		if record.ResponseStatus == "" {
			record.ResponseStatus = "http_error"
		}
	}
	if boundary == "provider" {
		state.addProvider(record)
	}
	r.write(record, state)
}

func (r *Recorder) write(record captureRecord, state *requestState) {
	var encoded []byte
	var encodeErr error
	if r.file != nil {
		encoded, encodeErr = json.Marshal(record, jsonv1.DefaultOptionsV1())
		encoded = append(encoded, '\n')
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics.Capture.Records++
	if record.CaptureError != "" {
		r.metrics.Capture.CaptureErrors++
	}
	if !record.ResponseComplete {
		r.metrics.Capture.Incomplete++
	}
	if record.Boundary == "codex" {
		r.addExchange(record, state.providerRecords())
	} else {
		addWebSocketControl(&r.metrics.Transport, record)
	}
	if encodeErr != nil {
		r.metrics.Capture.WriteErrors++
		return
	}
	if r.file != nil {
		if _, err := r.writes.Writer(r.file).Write(encoded); err != nil {
			r.metrics.Capture.WriteErrors++
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type observedReadCloser struct {
	io.ReadCloser
	content   bytes.Buffer
	readError error
}

func (body *observedReadCloser) Read(destination []byte) (int, error) {
	count, err := body.ReadCloser.Read(destination)
	if count != 0 {
		_, _ = body.content.Write(destination[:count])
	}
	if err != nil && !errors.Is(err, io.EOF) {
		body.readError = err
	}
	return count, err
}

type observedResponseBody struct {
	io.ReadCloser
	mu       sync.Mutex
	content  boundedObservation
	finish   func(observedPayload, error)
	finished bool
	readErr  error
}

func (body *observedResponseBody) Read(destination []byte) (int, error) {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.finished {
		return 0, io.ErrClosedPipe
	}
	count, err := body.ReadCloser.Read(destination)
	if count != 0 {
		_, _ = body.content.Write(destination[:count])
	}
	if err != nil {
		if !errors.Is(err, io.EOF) {
			body.readErr = err
		}
	}
	// The router supplies the shared terminal usage observation after reading
	// the payload. Finalize on Close so that observation reaches this record.
	return count, err
}

func (body *observedResponseBody) Close() error {
	// Closing the transport must unblock an in-flight Read before we wait for
	// its observation updates. Finalization and buffer writes are serialized.
	err := body.ReadCloser.Close()
	body.mu.Lock()
	defer body.mu.Unlock()
	if !body.finished {
		body.finished = true
		body.finish(body.content.snapshot(), body.readErr)
	}
	return err
}

type observedResponseWriter struct {
	http.ResponseWriter
	content    boundedObservation
	status     int
	writeError error
}

type observedPayload struct {
	content  []byte
	bytes    uint64
	overflow bool
}

type boundedObservation struct {
	content  bytes.Buffer
	bytes    uint64
	overflow bool
}

func (observation *boundedObservation) Write(payload []byte) (int, error) {
	observation.bytes += uint64(len(payload))
	remaining := maxObservedResponseBytes - observation.content.Len()
	if remaining > 0 {
		_, _ = observation.content.Write(payload[:min(len(payload), remaining)])
	}
	if len(payload) > remaining {
		observation.overflow = true
	}
	return len(payload), nil
}

func (observation *boundedObservation) snapshot() observedPayload {
	return observedPayload{content: observation.content.Bytes(), bytes: observation.bytes, overflow: observation.overflow}
}

func (writer *observedResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *observedResponseWriter) Write(payload []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	count, err := writer.ResponseWriter.Write(payload)
	if count != 0 {
		_, _ = writer.content.Write(payload[:count])
	}
	if err != nil {
		writer.writeError = err
	}
	return count, err
}

func (writer *observedResponseWriter) FlushError() error {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *observedResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *observedResponseWriter) statusCode() int {
	if writer.status == 0 {
		return http.StatusOK
	}
	return writer.status
}

func snapshotRequestBody(request *http.Request) ([]byte, error) {
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			if request.Body != nil {
				err = errors.Join(err, request.Body.Close())
			}
			return nil, err
		}
		payload, readErr := io.ReadAll(body)
		if err := errors.Join(readErr, body.Close()); err != nil {
			if request.Body != nil {
				err = errors.Join(err, request.Body.Close())
			}
			return nil, err
		}
		return payload, nil
	}
	original := request.Body
	payload, readErr := io.ReadAll(original)
	closeErr := original.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(payload))
	return payload, nil
}

func randomCaptureID() string {
	var value [16]byte
	rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
