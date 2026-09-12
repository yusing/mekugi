package router

// Source: client.go:1:679 upstream forwarding, response transformation, and usage observation.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	codexBaseURL           = "https://chatgpt.com/backend-api/codex"
	codexClientIdentity    = "codex_cli_rs"
	chatGPTAccountIDHeader = "Chatgpt-Account-Id"

	defaultDialTimeout          = 5 * time.Second
	maxUpstreamSniffBytes       = 64 << 10
	maxUpstreamErrorDetailBytes = 8 << 10
	maxUpstreamRetries          = 5
	initialUpstreamRetryBackoff = 100 * time.Millisecond
	maxUpstreamRetryBackoff     = 30 * time.Second
	selectedModelAtCapacity     = "selected model is at capacity"
	codexBetaFeaturesHeader     = "x-codex-beta-features"
	codexResponsesLiteHeader    = "x-openai-internal-codex-responses-lite"
	openAISubagentHeader        = "x-openai-subagent"
	mekugiCaptureIDHeader       = "x-mekugi-capture-id"
	codexSessionIDHeader        = "Session_id"
	upstreamJSONBufferBytes     = 64 << 20
)

var (
	utf8BOM                      = []byte{0xef, 0xbb, 0xbf}
	errResponseTransform         = errors.New("transform upstream response")
	errResponseWrite             = errors.New("write downstream response")
	errUpstreamStreamIdleTimeout = errors.New("upstream response stream idle timeout")
)

type responseProvider interface {
	forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error)
}

type responseTransformer interface {
	TransformJSON([]byte) ([]byte, error)
	TransformSSE([]byte) ([][]byte, error)
	Finish(bool) error
}

type responseTransformerChain struct {
	first  responseTransformer
	second responseTransformer
}

func (chain responseTransformerChain) TransformJSON(payload []byte) ([]byte, error) {
	payload, err := chain.first.TransformJSON(payload)
	if err != nil {
		return nil, err
	}
	return chain.second.TransformJSON(payload)
}

func (chain responseTransformerChain) TransformSSE(payload []byte) ([][]byte, error) {
	payloads, err := chain.first.TransformSSE(payload)
	if err != nil {
		return nil, err
	}
	var transformed [][]byte
	for _, current := range payloads {
		visible, err := chain.second.TransformSSE(current)
		if err != nil {
			return nil, err
		}
		transformed = append(transformed, visible...)
	}
	return transformed, nil
}

// Optional draining keeps transforms without buffered answer events unchanged.
func flushResponseSSE(transformer responseTransformer) ([][]byte, error) {
	if flusher, ok := transformer.(interface{ FlushSSE() ([][]byte, error) }); ok {
		return flusher.FlushSSE()
	}
	return nil, nil
}

func (chain responseTransformerChain) FlushSSE() ([][]byte, error) {
	pending, err := flushResponseSSE(chain.first)
	if err != nil {
		return nil, err
	}
	var visible [][]byte
	for _, payload := range pending {
		events, err := chain.second.TransformSSE(payload)
		if err != nil {
			return visible, err
		}
		visible = append(visible, events...)
	}
	tail, err := flushResponseSSE(chain.second)
	return append(visible, tail...), err
}

func (chain responseTransformerChain) Finish(streamEvent bool) error {
	return errors.Join(chain.first.Finish(streamEvent), chain.second.Finish(streamEvent))
}

func composeResponseTransformers(first, second responseTransformer) responseTransformer {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	default:
		return responseTransformerChain{first: first, second: second}
	}
}

type providerClient struct {
	websockets        *providerWebSockets
	grok              *grokClient
	httpClient        *http.Client
	baseURL           string
	streamIdleTimeout time.Duration
}

func newProviderClient(baseURL string, supplied *http.Client) *providerClient {
	return &providerClient{httpClient: withDialTimeout(supplied), baseURL: baseURL}
}

// requiredCodexAuthHeaders enforces the router's authentication boundary.
// Codex owns login and token refresh; a custom provider configured with
// requires_openai_auth = true attaches its managed credentials to every request.
// The router validates those headers and relays them to the ChatGPT Codex backend.
func requiredCodexAuthHeaders(headers http.Header) (string, string, error) {
	authorizationValues := headers.Values("Authorization")
	if len(authorizationValues) != 1 {
		return "", "", errors.New("codex request must provide exactly one Authorization header; configure the provider with requires_openai_auth = true")
	}
	scheme, token, ok := strings.Cut(authorizationValues[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || token != strings.TrimSpace(token) {
		return "", "", errors.New("codex request has an invalid bearer Authorization header")
	}
	accountValues := headers.Values(chatGPTAccountIDHeader)
	if len(accountValues) != 1 || accountValues[0] == "" || accountValues[0] != strings.TrimSpace(accountValues[0]) {
		return "", "", errors.New("codex request must provide exactly one non-empty ChatGPT-Account-ID header")
	}
	return authorizationValues[0], accountValues[0], nil
}

func (c *providerClient) forwardModels(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
	authorization, accountID, err := requiredCodexAuthHeaders(headers)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build upstream models request: %w", err)
	}
	request.URL.RawQuery = rawQuery
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Accept", "application/json")
	request.Header.Set(chatGPTAccountIDHeader, accountID)
	request.Header.Set("Originator", codexClientIdentity)
	request.Header.Set("User-Agent", codexClientIdentity)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("forward upstream models request: %w", err)
	}
	return response, nil
}

func (c *providerClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	var model struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, errors.New("invalid provider request")
	}
	if isGrokModel(model.Model) {
		if c.grok == nil {
			return nil, errors.New("Grok subagents require --grok")
		}
		return c.grok.forwardExecution(startCtx, responseCtx, body, headers)
	}

	authorization, accountID, err := requiredCodexAuthHeaders(headers)
	if err != nil {
		return nil, err
	}
	if c.websockets != nil {
		upstreamHeaders := http.Header{}
		upstreamHeaders.Set("Authorization", authorization)
		upstreamHeaders.Set(chatGPTAccountIDHeader, accountID)
		upstreamHeaders.Set("Originator", codexClientIdentity)
		upstreamHeaders.Set("User-Agent", codexClientIdentity)
		forwardCodexRequestHeaders(upstreamHeaders, headers)
		if validCodexCacheKey(cacheKey) {
			upstreamHeaders[codexSessionIDHeader] = []string{cacheKey}
		}
		response, fallback, err := c.forwardWebSocket(startCtx, responseCtx, body, upstreamHeaders, cacheKey)
		if !fallback {
			return response, err
		}
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/responses"
	for attempt := 0; ; attempt++ {
		requestCtx, cancelRequest := context.WithCancel(responseCtx)
		stopStartCancellation := context.AfterFunc(startCtx, cancelRequest)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			stopStartCancellation()
			cancelRequest()
			return nil, fmt.Errorf("build upstream request: %w", err)
		}
		defaults := []struct {
			name  string
			value string
		}{
			{"Authorization", authorization},
			{"Content-Type", "application/json"},
			{"Accept", "application/json, text/event-stream"},
			{chatGPTAccountIDHeader, accountID},
			{"Originator", codexClientIdentity},
			{"User-Agent", codexClientIdentity},
		}
		for _, header := range defaults {
			request.Header.Set(header.name, header.value)
		}
		forwardCodexRequestHeaders(request.Header, headers)
		if validCodexCacheKey(cacheKey) {
			request.Header[codexSessionIDHeader] = []string{cacheKey}
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			stopStartCancellation()
			cancelRequest()
			return nil, fmt.Errorf("forward upstream request: %w", err)
		}
		finishResponseStart := func() error {
			if !stopStartCancellation() {
				cancelRequest()
				_ = response.Body.Close()
				if err := startCtx.Err(); err != nil {
					return err
				}
				return context.Canceled
			}
			var body io.ReadCloser = &cancelOnCloseReadCloser{body: response.Body, cancel: cancelRequest}
			if c.streamIdleTimeout > 0 {
				body = newStreamIdleReadCloser(responseCtx, body, c.streamIdleTimeout)
			}
			response.Body = body
			return nil
		}
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			if err := finishResponseStart(); err != nil {
				return nil, fmt.Errorf("forward upstream request: %w", err)
			}
			return response, nil
		}
		recoverable := responseHasRecoverableUpstreamError(response)
		if err := finishResponseStart(); err != nil {
			return nil, fmt.Errorf("forward upstream request: %w", err)
		}
		if !recoverable || attempt == maxUpstreamRetries {
			return response, nil
		}
		delay := upstreamRetryDelay(response, attempt)
		_ = response.Body.Close()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-startCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait to retry upstream request: %w", startCtx.Err())
		}
	}
}

func forwardCodexRequestHeaders(destination, source http.Header) {
	for _, name := range []string{
		threadIDHeader,
		clientRequestIDHeader,
		codexWindowIDHeader,
		codexBetaFeaturesHeader,
		codexResponsesLiteHeader,
		openAISubagentHeader,
		codexTurnMetadataHeader,
		// Codex owns this provider-issued, per-turn sticky-routing token. Relay
		// it unchanged; never derive it from or retain it with the session key.
		"x-codex-turn-state",
		mekugiCaptureIDHeader,
	} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

func validCodexCacheKey(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if (char < 0x20 && char != '\t') || char == 0x7f {
			return false
		}
	}
	return true
}

type cancelOnCloseReadCloser struct {
	body   io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReadCloser) Read(body []byte) (int, error) {
	return r.body.Read(body)
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.body.Close()
	r.cancel()
	return err
}

type streamIdleReadCloser struct {
	ctx       context.Context
	body      io.ReadCloser
	timeout   time.Duration
	timedOut  atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func newStreamIdleReadCloser(ctx context.Context, body io.ReadCloser, timeout time.Duration) *streamIdleReadCloser {
	return &streamIdleReadCloser{ctx: ctx, body: body, timeout: timeout}
}

func (r *streamIdleReadCloser) Read(buffer []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, errUpstreamStreamIdleTimeout
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(r.timeout, func() {
		r.timedOut.Store(true)
		_ = r.closeBody()
		close(expired)
	})
	read, err := r.body.Read(buffer)
	if !timer.Stop() {
		<-expired
	}
	if r.timedOut.Load() {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return read, errors.Join(contextErr, errUpstreamStreamIdleTimeout, err)
		}
		return read, errors.Join(errUpstreamStreamIdleTimeout, err)
	}
	return read, err
}

func (r *streamIdleReadCloser) closeBody() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.body.Close()
	})
	return r.closeErr
}

func (r *streamIdleReadCloser) Close() error {
	return r.closeBody()
}

func upstreamRetryDelay(response *http.Response, attempt int) time.Duration {
	if value := strings.TrimSpace(response.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
			if seconds >= uint64(maxUpstreamRetryBackoff/time.Second) {
				return maxUpstreamRetryBackoff
			}
			return min(time.Duration(seconds)*time.Second, maxUpstreamRetryBackoff)
		}
		if retryAt, err := http.ParseTime(value); err == nil {
			return min(max(time.Until(retryAt), 0), maxUpstreamRetryBackoff)
		}
	}
	delay := min(initialUpstreamRetryBackoff<<attempt, maxUpstreamRetryBackoff)
	half := delay / 2
	return half + rand.N(delay-half)
}

func responseHasRecoverableUpstreamError(response *http.Response) bool {
	if response == nil || response.Body == nil || response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return false
	}
	detail, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorDetailBytes+1))
	replayResponseBody(response, detail, response.Body)
	if err != nil || len(detail) > maxUpstreamErrorDetailBytes {
		return false
	}
	message := strings.ToLower(upstreamErrorMessage(detail))
	return strings.Contains(message, selectedModelAtCapacity)
}

func upstreamErrorMessage(body []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) == 0 {
		return ""
	}
	var providerError struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(envelope.Error, &providerError) == nil {
		return strings.TrimSpace(providerError.Message)
	}
	var message string
	if json.Unmarshal(envelope.Error, &message) == nil {
		return strings.TrimSpace(message)
	}
	return ""
}

func prepareUpstreamBody(response *http.Response, streamRequested bool) (bool, error) {
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return true, nil
	}
	if !streamRequested {
		return false, nil
	}

	probe := bufio.NewReader(io.LimitReader(response.Body, maxUpstreamSniffBytes+1))
	var prefix bytes.Buffer
	defer func() {
		replayResponseBody(response, prefix.Bytes(), io.MultiReader(probe, response.Body))
	}()
	firstLine := true
	for {
		line, err := probe.ReadString('\n')
		prefix.WriteString(line)
		if prefix.Len() > maxUpstreamSniffBytes {
			return false, nil
		}
		content, _ := trimSSELineEnding(line)
		if firstLine {
			content = strings.TrimPrefix(content, "\uFEFF")
			firstLine = false
		}
		if content != "" {
			if isSSELine(content) {
				response.Header.Set("Content-Type", "text/event-stream")
				return true, nil
			}
			jsonPrefix := strings.TrimSpace(content)
			if strings.HasPrefix(jsonPrefix, "{") || strings.HasPrefix(jsonPrefix, "[") {
				return false, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
}

func replayResponseBody(response *http.Response, prefix []byte, remainder io.Reader) {
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(prefix), remainder), response.Body}
}

func isSSELine(line string) bool {
	if strings.HasPrefix(line, ":") {
		return true
	}
	field, _, _ := strings.Cut(line, ":")
	switch field {
	case "data", "event", "id", "retry":
		return true
	default:
		return false
	}
}

type responseTerminalState uint8

const (
	responseTerminalUnknown responseTerminalState = iota
	responseTerminalInvalid
	responseTerminalPending
	responseTerminalCompleted
	responseTerminalSteered
	responseTerminalFailed
)

func (state responseTerminalState) String() string {
	switch state {
	case responseTerminalInvalid:
		return "invalid"
	case responseTerminalPending:
		return "pending"
	case responseTerminalCompleted:
		return "completed"
	case responseTerminalSteered:
		return "steered"
	case responseTerminalFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func copyUpstreamBodyTransformed(writer io.Writer, response *http.Response, streamResponse bool, transformer responseTransformer, observeUsage func(tokenCounts)) (responseTerminalState, error) {
	defer response.Body.Close()
	var (
		terminalState responseTerminalState
		err           error
	)
	if streamResponse {
		terminalState, err = copySSETransformed(writer, response.Body, transformer, observeUsage)
	} else {
		terminalState, err = copyJSONTransformed(writer, response.Body, transformer, observeUsage)
	}
	if err != nil {
		return terminalState, fmt.Errorf("copy upstream response: %w", err)
	}
	return terminalState, nil
}

func copyJSONTransformed(writer io.Writer, reader io.Reader, transformer responseTransformer, observeUsage func(tokenCounts)) (responseTerminalState, error) {
	body, err := io.ReadAll(io.LimitReader(reader, upstreamJSONBufferBytes+1))
	if err != nil {
		return responseTerminalUnknown, err
	}
	if len(body) > upstreamJSONBufferBytes {
		return responseTerminalUnknown, fmt.Errorf("upstream JSON response exceeds the router buffer budget")
	}
	recordObservedUsage(body, false, observeUsage)
	visible := body
	if transformer != nil {
		visible, err = transformer.TransformJSON(body)
		if err != nil {
			return responseTerminalUnknown, fmt.Errorf("%w: %w", errResponseTransform, err)
		}
		if err := transformer.Finish(false); err != nil {
			return responseTerminalUnknown, fmt.Errorf("%w: %w", errResponseTransform, err)
		}
	}
	terminalState := observeResponseTerminal(visible, false)
	if _, err = writer.Write(visible); err != nil {
		return responseTerminalUnknown, fmt.Errorf("%w: %w", errResponseWrite, err)
	}
	return terminalState, nil
}

func copySSETransformed(writer io.Writer, reader io.Reader, transformer responseTransformer, observeUsage func(tokenCounts)) (state responseTerminalState, resultErr error) {
	defer func() {
		if errors.Is(resultErr, errResponseWrite) {
			return // The downstream is no longer writable.
		}
		pending, err := flushResponseSSE(transformer)
		resultErr = errors.Join(resultErr, err)
		for _, payload := range pending {
			_, err := writeSSEEvent(writer, responseSSELines(payload, "\n"), "\n", nil, nil)
			if err != nil {
				resultErr = errors.Join(resultErr, err)
				return
			}
		}
	}()
	buffered := bufio.NewReader(reader)

	if err := consumeOptionalUTF8BOM(buffered); err != nil {
		return responseTerminalUnknown, err
	}
	event := []string{}
	terminalState := responseTerminalUnknown
	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			if line == "\n" || line == "\r\n" {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, line, transformer, observeUsage)
				terminalState = mergeResponseTerminalState(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				event = event[:0]
				if isResponseTerminal(eventTerminalState) {
					return terminalState, nil
				}
			} else {
				event = append(event, line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, "", transformer, observeUsage)
				terminalState = mergeResponseTerminalState(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				if isResponseTerminal(eventTerminalState) {
					return terminalState, nil
				}
				if transformer != nil {
					if finishErr := transformer.Finish(true); finishErr != nil {
						return terminalState, fmt.Errorf("%w: %w", errResponseTransform, finishErr)
					}
				}
				return terminalState, nil
			}
			return terminalState, err
		}
	}
}

func isResponseTerminal(state responseTerminalState) bool {
	return state == responseTerminalCompleted || state == responseTerminalFailed || state == responseTerminalSteered
}

func consumeOptionalUTF8BOM(reader *bufio.Reader) error {
	prefix, err := reader.Peek(len(utf8BOM))
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if bytes.Equal(prefix, utf8BOM) {
		_, err = reader.Discard(len(utf8BOM))
		return err
	}
	return nil
}

func writeSSEEvent(writer io.Writer, lines []string, separator string, transformer responseTransformer, observeUsage func(tokenCounts)) (responseTerminalState, error) {
	defer releaseResponseDelivery(transformer)
	if len(lines) == 0 {
		if separator != "" {
			if _, err := io.WriteString(writer, separator); err != nil {
				return responseTerminalUnknown, fmt.Errorf("%w: %w", errResponseWrite, err)
			}
		}
		if responseWriter, ok := writer.(http.ResponseWriter); ok {
			if err := http.NewResponseController(responseWriter).Flush(); err != nil {
				return responseTerminalUnknown, fmt.Errorf("%w: %w", errResponseWrite, err)
			}
		}
		return responseTerminalUnknown, nil
	}
	payload := ssePayload(lines)
	terminalState := observeResponseTerminal(payload, true)

	recordObservedUsage(payload, true, observeUsage)
	visible := [][]byte{payload}
	if transformer != nil && len(payload) > 0 {
		var err error
		visible, err = transformer.TransformSSE(payload)
		if err != nil {
			return terminalState, fmt.Errorf("%w: %w", errResponseTransform, err)
		}
	}
	if len(visible) == 0 {
		return terminalState, nil
	}

	var originalEnvelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &originalEnvelope)
	for index, eventPayload := range visible {
		eventLines := lines
		eventSeparator := separator
		var transformedEnvelope struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(eventPayload, &transformedEnvelope)
		if len(visible) > 1 || transformedEnvelope.Type != originalEnvelope.Type {
			lineEnding := "\n"
			if separator == "\r\n" {
				lineEnding = "\r\n"
			}
			eventLines = responseSSELines(eventPayload, lineEnding)
			if index < len(visible)-1 && eventSeparator == "" {
				eventSeparator = lineEnding
			}
		}
		augmented := encodeSSEEventPayload(eventLines, eventPayload)
		terminalState = mergeResponseTerminalState(terminalState, observeResponseTerminal(eventPayload, true))
		if _, err := io.WriteString(writer, augmented); err != nil {
			return terminalState, fmt.Errorf("%w: %w", errResponseWrite, err)
		}
		if eventSeparator != "" {
			if _, err := io.WriteString(writer, eventSeparator); err != nil {
				return terminalState, fmt.Errorf("%w: %w", errResponseWrite, err)
			}
		}
	}
	if responseWriter, ok := writer.(http.ResponseWriter); ok {
		if err := http.NewResponseController(responseWriter).Flush(); err != nil {
			return terminalState, fmt.Errorf("%w: %w", errResponseWrite, err)
		}
	}
	for _, payload := range visible {
		confirmResponseDelivery(transformer, payload)
	}
	return terminalState, nil
}

func ssePayload(lines []string) []byte {
	parts := []string{}
	for _, line := range lines {
		content, _ := trimSSELineEnding(line)
		if part, ok := strings.CutPrefix(content, "data:"); ok {
			parts = append(parts, strings.TrimPrefix(part, " "))
		}
	}
	return []byte(strings.Join(parts, "\n"))
}

func recordObservedUsage(payload []byte, streamEvent bool, observe func(tokenCounts)) {
	if observe == nil {
		return
	}
	counts, ok := usageFromResponsePayload(payload, streamEvent)
	if !ok {
		return
	}
	observe(counts)
}

// Synthesized frames need the same event name and data framing as live SSE,
// including when buffered answer events are released after an interrupted stream.
func responseSSELines(payload []byte, ending string) []string {
	var envelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &envelope)
	var lines []string
	if envelope.Type != "" {
		lines = append(lines, "event: "+envelope.Type+ending)
	}
	for line := range bytes.SplitSeq(payload, []byte("\n")) {
		lines = append(lines, "data: "+string(line)+ending)
	}
	return lines
}

func encodeSSEEventPayload(lines []string, payload []byte) string {
	dataIndexes := []int{}
	for index, line := range lines {
		content, _ := trimSSELineEnding(line)
		if !strings.HasPrefix(content, "data:") {
			continue
		}
		dataIndexes = append(dataIndexes, index)
	}
	if len(dataIndexes) == 0 {
		return strings.Join(lines, "")
	}
	if strings.TrimSpace(string(payload)) == "" || strings.TrimSpace(string(payload)) == "[DONE]" {
		return strings.Join(lines, "")
	}

	firstData := dataIndexes[0]
	dataSet := map[int]bool{}
	for _, index := range dataIndexes {
		dataSet[index] = true
	}
	var result strings.Builder
	for index, line := range lines {
		if index == firstData {
			_, ending := trimSSELineEnding(line)
			// Each physical payload line needs its own data field. Preserve an
			// unterminated final line, but separate any preceding payload lines.
			first := true
			for part := range bytes.SplitSeq(payload, []byte("\n")) {
				if !first {
					if ending == "" {
						result.WriteByte('\n')
					} else {
						result.WriteString(ending)
					}
				}
				first = false
				result.WriteString("data: ")
				result.Write(part)
			}
			result.WriteString(ending)

			continue
		}
		if !dataSet[index] {
			result.WriteString(line)
		}
	}
	return result.String()
}

// Source: codex-rs/codex-api/src/endpoint/responses_websocket.rs:749:777 and
// codex-rs/codex-api/src/sse/responses.rs:524:535. These carry metadata rather
// than response acceptance or a terminal state.
func isResponseAncillaryEvent(kind string) bool {
	switch kind {
	case "codex.response.metadata", "codex.rate_limits", "responsesapi.websocket_timing":
		return true
	default:
		return false
	}
}

func observeResponseTerminal(body []byte, streamEvent bool) responseTerminalState {
	if streamEvent {
		payload := strings.TrimSpace(string(body))
		if payload == "" || payload == "[DONE]" {
			return responseTerminalUnknown
		}
	}
	var envelope struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		if streamEvent {
			return responseTerminalInvalid
		}
		return responseTerminalUnknown
	}
	status := envelope.Status
	if streamEvent {
		if isResponseAncillaryEvent(envelope.Type) {
			return responseTerminalUnknown
		}
		if !strings.HasPrefix(envelope.Type, "response.") || envelope.Type == "response." {
			return responseTerminalInvalid
		}
		status = strings.TrimPrefix(envelope.Type, "response.")
	}
	switch status {
	case "completed":
		return responseTerminalCompleted
	case "incomplete":
		var event struct {
			Response struct {
				Incomplete struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if streamEvent && json.Unmarshal(body, &event) == nil && event.Response.Incomplete.Reason == "steered" {
			return responseTerminalSteered
		}
		return responseTerminalFailed
	case "failed":
		return responseTerminalFailed
	case "queued", "in_progress":
		return responseTerminalPending
	default:
		if streamEvent {
			return responseTerminalPending
		}
		return responseTerminalUnknown
	}
}

func mergeResponseTerminalState(current, observed responseTerminalState) responseTerminalState {
	if current == responseTerminalInvalid || observed == responseTerminalInvalid {
		return responseTerminalInvalid
	}
	if observed == responseTerminalUnknown {
		return current
	}
	if current == responseTerminalFailed || observed == responseTerminalFailed {
		return responseTerminalFailed
	}
	if current == responseTerminalSteered || observed == responseTerminalSteered {
		return responseTerminalSteered
	}
	if current == responseTerminalCompleted || observed == responseTerminalCompleted {
		return responseTerminalCompleted
	}
	return responseTerminalPending
}

func trimSSELineEnding(line string) (string, string) {
	if before, ok := strings.CutSuffix(line, "\r\n"); ok {
		return before, "\r\n"
	}
	if before, ok := strings.CutSuffix(line, "\n"); ok {
		return before, "\n"
	}
	return line, ""
}

func withDialTimeout(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	if cloned.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext
		cloned.Transport = transport
	}
	return &cloned
}
