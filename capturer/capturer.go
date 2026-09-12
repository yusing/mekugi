// Package capturer observes the router's existing client and provider seams
// without adding a listener or changing request and response bytes.
package capturer

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

const schemaVersion = 6

// Detailed exchanges are diagnostic evidence rather than the cumulative
// counters. Keeping a fixed recent window prevents an always-on router from
// retaining request history without bound; overflow is explicit capture
// health so benchmark validation fails rather than using incomplete detail.
const maxRetainedExchangeDetails = 4096

const maxObservedResponseBytes = 8 << 20

// The router-owned marker identifies the carrier independently of wrappers
// around the host call, such as change-ID failure reporting.
const mekugiApplyCarrierPrefix = "// mekugi-proxy: apply translated patch\n"

const (
	mekugiNativeApplyCarrierPrefix      = "# mekugi-proxy: apply translated patch\n"
	mekugiNativeReportCarrierPrefix     = "# mekugi-proxy: return mekugi report\n"
	mekugiNativeDiagnosticCarrierPrefix = "# mekugi-proxy: return mekugi diagnostic "
)

type captureKey struct{}

// Config identifies the observed router behavior and optional durable JSONL
// destination. An empty Output retains records only for the process lifetime.
type Config struct {
	Output        string
	Mode          string
	ModelProtocol string
}

// ProviderUsage is the provider-authoritative token count parsed from one
// terminal Responses payload.
type ProviderUsage struct {
	InputTokens     uint64 `json:"input_tokens"`
	CachedTokens    uint64 `json:"cached_input_tokens"`
	OutputTokens    uint64 `json:"output_tokens"`
	ReasoningTokens uint64 `json:"reasoning_tokens"`
}

type payloadMetrics struct {
	Bytes  uint64 `json:"bytes"`
	Tokens uint64 `json:"tokens"`
}

type toolCallMetrics struct {
	CallID      string `json:"call_id"`
	Name        string `json:"name"`
	InputBytes  uint64 `json:"input_bytes"`
	InputTokens uint64 `json:"input_tokens"`
	ItemBytes   uint64 `json:"item_bytes"`
	ItemTokens  uint64 `json:"item_tokens"`
	Kind        string `json:"kind,omitempty"`
	Diagnostic  string `json:"diagnostic,omitempty"`
}

type captureRecord struct {
	Transport           string                             `json:"transport,omitempty"`
	ControlDirection    ResponsesWebSocketControlDirection `json:"control_direction,omitempty"`
	InstructionRewrite  *InstructionRewrite                `json:"instruction_rewrite,omitempty"`
	ProviderResponse    *providerResponseEvidence          `json:"provider_response,omitempty"`
	PredecessorSequence uint64                             `json:"predecessor_sequence,omitempty"`
	SchemaVersion       int                                `json:"schema_version"`
	Boundary            string                             `json:"boundary"`
	CaptureID           string                             `json:"capture_id"`
	RequestSequence     uint64                             `json:"request_sequence"`
	ProviderAttempt     uint64                             `json:"provider_attempt,omitempty"`
	Mode                string                             `json:"mode"`
	ModelProtocol       string                             `json:"model_protocol"`
	RequestID           string                             `json:"request_id,omitempty"`
	SessionID           string                             `json:"session_id,omitempty"`
	ThreadID            string                             `json:"thread_id,omitempty"`
	Subagent            string                             `json:"subagent,omitempty"`
	RequestKind         string                             `json:"request_kind,omitempty"`
	ProviderExpected    *bool                              `json:"provider_expected,omitempty"`
	RequestModel        string                             `json:"request_model,omitempty"`
	Request             payloadMetrics                     `json:"request"`
	Fingerprint         *requestFingerprint                `json:"cache_fingerprint,omitempty"`
	NativeFingerprint   *requestFingerprint                `json:"native_fingerprint,omitempty"`
	NativeRequest       *payloadMetrics                    `json:"native_request,omitempty"`
	RequestTools        []string                           `json:"request_tools,omitempty"`
	StatusCode          int                                `json:"status_code"`
	ResponseComplete    bool                               `json:"response_complete"`
	ResponseStatus      string                             `json:"response_status,omitempty"`
	Usage               *ProviderUsage                     `json:"usage,omitempty"`
	ToolCalls           []toolCallMetrics                  `json:"tool_calls,omitempty"`
	Response            payloadMetrics                     `json:"response"`
	FinalOutput         payloadMetrics                     `json:"final_output,omitzero"`
	FinalText           payloadMetrics                     `json:"final_text,omitzero"`
	CaptureError        string                             `json:"capture_error,omitempty"`
	DurationMillis      uint64                             `json:"duration_ms"`
	CapturedAt          time.Time                          `json:"captured_at"`
}

// Recorder owns correlation, sanitized measurement, durable capture, and
// derived metrics for one router process.
type Recorder struct {
	lastRequestSequence map[string]uint64
	fingerprintKey      [32]byte
	mu                  sync.Mutex
	file                *os.File
	codec               tokenizer.Codec
	mode                string
	modelProtocol       string
	requestSequence     uint64
	metrics             metricsSnapshot
	previousInput       map[string]uint64
	cacheQueues         map[string][]*requestState
}

type requestState struct {
	requestKind         string
	instructionRewrite  *InstructionRewrite
	predecessorSequence uint64
	recorder            *Recorder
	nativeRequest       *payloadMetrics
	nativeRequestError  bool
	nativeFingerprint   *requestFingerprint
	providerRouting     map[uint64]requestRouting
	clientTurnState     string
	mu                  sync.Mutex
	captureID           string
	sequence            uint64
	providerAttempts    uint64
	requestID           string
	sessionID           string
	threadID            string
	subagent            string
	providers           []captureRecord
	providerUsage       map[uint64]ProviderUsage
	cacheReady          bool
	cacheUsage          *usageMetrics
}

type requestRouting struct {
	sessionKey string
	turnState  string
}

// ObserveRequestKind records only the router's validated, content-free request kind.
func ObserveRequestKind(ctx context.Context, kind string) {
	switch kind {
	case "turn", "prewarm", "compaction":
	default:
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

// ObserveNativeRequest supplies the actual request after replay/tool projection
// and before CTP encoding. Measurement is capture-owned; no raw body is retained.
// It is observation only and cannot fail or change the routed operation.
func ObserveNativeRequest(ctx context.Context, body []byte) {
	state, ok := ctx.Value(captureKey{}).(*requestState)
	if !ok {
		return
	}
	measured, err := state.recorder.measure(body)
	fingerprint := state.recorder.requestFingerprint(body)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.nativeRequest = &measured
	state.nativeFingerprint = fingerprint
	state.nativeRequestError = err != nil
}

// New creates one in-process recorder. It never starts a server.
func New(config Config) (*Recorder, error) {
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		return nil, fmt.Errorf("load capture tokenizer: %w", err)
	}
	var file *os.File
	if config.Output != "" {
		file, err = os.OpenFile(config.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open capture output: %w", err)
		}
	}
	var fingerprintKey [32]byte
	if _, err := rand.Read(fingerprintKey[:]); err != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, fmt.Errorf("initialize private cache fingerprints: %w", err)
	}
	return &Recorder{
		fingerprintKey: fingerprintKey, file: file, codec: codec, mode: config.Mode, modelProtocol: config.ModelProtocol,
		lastRequestSequence: make(map[string]uint64),
		metrics:             newMetricsSnapshot(config.Mode, config.ModelProtocol),
		previousInput:       make(map[string]uint64),
		cacheQueues:         make(map[string][]*requestState),
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
			next.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			next.ServeHTTP(writer, request)
			return
		}
		state, err := r.beginRequest(request.Header)
		if err != nil {
			r.mu.Lock()
			r.metrics.Capture.SkippedRequests++
			r.mu.Unlock()
			next.ServeHTTP(writer, request)
			return
		}
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
		if !ok || request.Method != http.MethodPost || !(strings.HasSuffix(request.URL.Path, "/responses") || strings.HasSuffix(request.URL.Path, "/chat/completions")) {
			return next.RoundTrip(request)
		}
		attempt := state.beginProviderAttempt()
		requestBody, err := snapshotRequestBody(request)
		if err != nil {
			return nil, fmt.Errorf("capture provider request body: %w", err)
		}
		state.observeProviderRouting(attempt, request.Header)
		started := time.Now()
		response, roundTripErr := next.RoundTrip(request)
		if roundTripErr != nil {
			r.recordExchange(state, "provider", attempt, started, requestBody, observedPayload{}, 0, "", "", roundTripErr, providerResponseEvidence{CachedTokensState: "unavailable"})
			return nil, roundTripErr
		}
		contentType := response.Header.Get("Content-Type")
		contentEncoding := response.Header.Get("Content-Encoding")
		statusCode := response.StatusCode
		evidence := providerHeaderEvidence(response.Header)
		response.Body = &observedResponseBody{
			ReadCloser: response.Body,
			finish: func(body observedPayload, readErr error) {
				r.recordExchange(state, "provider", attempt, started, requestBody, body, statusCode, contentType, contentEncoding, readErr, evidence)
			},
		}
		return response, nil
	})
}

func (r *Recorder) beginRequest(header http.Header) (*requestState, error) {
	captureID, err := randomCaptureID()
	if err != nil {
		return nil, err
	}
	state := &requestState{
		captureID:       captureID,
		recorder:        r,
		clientTurnState: r.turnStateFingerprint(header.Get("x-codex-turn-state")),
		requestID:       header.Get("x-client-request-id"),
		sessionID:       cmp.Or(header.Get("session-id"), header.Get("Session_id")),
		threadID:        header.Get("thread-id"),
		subagent:        header.Get("x-openai-subagent"),
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
		r.cacheQueues[state.threadID] = append(r.cacheQueues[state.threadID], state)
	}
	r.mu.Unlock()
	return state, nil
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

func (r *Recorder) recordExchange(state *requestState, boundary string, attempt uint64, started time.Time, requestBody []byte, responseBody observedPayload, statusCode int, contentType, contentEncoding string, exchangeErr error, evidence providerResponseEvidence) {
	record := captureRecord{
		SchemaVersion:   schemaVersion,
		Boundary:        boundary,
		CaptureID:       state.captureID,
		RequestSequence: state.sequence, PredecessorSequence: state.predecessorSequence,
		ProviderAttempt: attempt,
		Mode:            r.mode,
		ModelProtocol:   r.modelProtocol,
		RequestID:       state.requestID,
		SessionID:       state.sessionID,
		ThreadID:        state.threadID,
		Subagent:        state.subagent,
		StatusCode:      statusCode,
		DurationMillis:  uint64(max(time.Since(started).Milliseconds(), 0)),
		CapturedAt:      time.Now().UTC(),
		Usage:           state.observedUsage(boundary, attempt),
	}
	state.mu.Lock()
	record.RequestKind = state.requestKind
	if state.instructionRewrite != nil {
		record.InstructionRewrite = new(*state.instructionRewrite)
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
	if measured, err := r.measure(requestBody); err != nil {
		record.CaptureError = "measure request payload"
	} else {
		record.Request = measured
	}
	record.Fingerprint = r.requestFingerprint(requestBody)
	if boundary == "codex" && record.Fingerprint != nil {
		value := state.clientTurnState
		record.Fingerprint.TurnState = &value
	}
	if boundary == "provider" {
		state.mu.Lock()
		record.NativeFingerprint = state.nativeFingerprint
		if record.Fingerprint != nil {
			if routing, ok := state.providerRouting[attempt]; ok {
				record.Fingerprint.RoutingKey = routing.sessionKey
				record.Fingerprint.TurnState = &routing.turnState
			}
		}

		if state.nativeRequest != nil {
			baseline := *state.nativeRequest
			record.NativeRequest = &baseline
		}
		baselineError := state.nativeRequestError
		state.mu.Unlock()
		if baselineError {
			record.CaptureError = "measure native request"
		}
		if record.NativeRequest == nil {
			if r.modelProtocol == "ctp2" {
				record.CaptureError = "missing native request observation"
			} else {
				baseline := record.Request
				record.NativeRequest = &baseline
				record.NativeFingerprint = cloneFingerprint(record.Fingerprint)
				if record.NativeFingerprint != nil {
					record.NativeFingerprint.RoutingKey = ""
					record.NativeFingerprint.TurnState = nil
				}
			}
		}
	}
	var requestEnvelope struct {
		Generate *bool             `json:"generate"`
		Model    string            `json:"model"`
		Messages json.RawMessage   `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if len(requestBody) != 0 {
		if err := json.Unmarshal(requestBody, &requestEnvelope); err != nil {
			record.CaptureError = "invalid request JSON"
		} else {
			record.RequestModel = requestEnvelope.Model
			if boundary == "codex" && requestEnvelope.Generate != nil && !*requestEnvelope.Generate {
				record.ProviderExpected = new(false)
			}
			record.RequestTools = requestToolNames(requestEnvelope.Tools)
			if strings.TrimSpace(requestEnvelope.Model) == "" {
				record.CaptureError = "missing request model"
			}
		}
	}
	record.Response.Bytes = responseBody.bytes
	observedContent, err := decodedCapturePayload(responseBody.content, contentEncoding)
	if responseBody.overflow || errors.Is(err, errDecodedPayloadTooLarge) {
		record.CaptureError = "response payload exceeds capture observation limit"
	} else if err != nil {
		record.CaptureError = "unsupported or invalid response content encoding"
	} else if len(observedContent) != 0 {
		if measured, measureErr := r.measureResponse(observedContent, contentType); measureErr != nil {
			record.CaptureError = "measure response payload"
		} else {
			record.Response.Tokens = measured.Tokens
			lowerContentType := strings.ToLower(contentType)
			if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices ||
				strings.Contains(lowerContentType, "json") || strings.Contains(lowerContentType, "text/event-stream") ||
				capturedPayloadLooksLikeSSE(observedContent) {
				var finalOutput []byte
				if len(requestEnvelope.Messages) > 0 {
					finalOutput = observeChatResponse(observedContent, contentType, &record, r.codec)
				} else {
					finalOutput = observeResponse(observedContent, contentType, &record, r.codec)
				}
				if len(finalOutput) != 0 {
					if finalMeasured, finalMeasureErr := r.measure(finalOutput); finalMeasureErr != nil {
						record.CaptureError = "measure final output payload"
					} else {
						record.FinalOutput = finalMeasured
						if textMetrics, textErr := measureOutputText(finalOutput, r.codec); textErr != nil {
							record.CaptureError = "measure output text"
						} else {
							record.FinalText = textMetrics
						}
					}
				}
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

func (r *Recorder) measure(payload []byte) (payloadMetrics, error) {
	count, err := contentTokens(payload, r.codec)
	if err != nil {
		return payloadMetrics{}, err
	}
	if count < 0 {
		return payloadMetrics{}, errors.New("capture tokenizer returned a negative count")
	}
	return payloadMetrics{Bytes: uint64(len(payload)), Tokens: uint64(count)}, nil
}

func (r *Recorder) write(record captureRecord, state *requestState) {
	var encoded []byte
	var encodeErr error
	if r.file != nil {
		encoded, encodeErr = json.Marshal(record)
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
		r.addExchange(record, state, state.providerRecords())
	} else {
		addWebSocketControl(&r.metrics.Transport, record)
	}
	if encodeErr != nil {
		r.metrics.Capture.WriteErrors++
		return
	}
	if r.file != nil {
		if _, err := r.file.Write(encoded); err != nil {
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

func randomCaptureID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (r *Recorder) measureResponse(payload []byte, contentType string) (payloadMetrics, error) {
	if contentType != webSocketContentType {
		return r.measure(payload)
	}
	measured := payloadMetrics{Bytes: uint64(len(payload))}
	for message := range webSocketMessages(payload) {
		value, err := r.measure(message)
		if err != nil {
			return payloadMetrics{}, err
		}
		measured.Tokens += value.Tokens
	}
	return measured, nil
}
