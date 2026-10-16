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
	"mime"
	"net/http"
	"strings"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/libraries/httptransport"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const (
	maxUpstreamSniffBytes       = 64 << 10
	maxUpstreamErrorDetailBytes = 8 << 10
	upstreamJSONBufferBytes     = 64 << 20
)

var (
	utf8BOM              = []byte{0xef, 0xbb, 0xbf}
	errResponseTransform = errors.New("transform upstream response")
	errResponseWrite     = errors.New("write downstream response")
)

type providerClient struct {
	openai         *openai.Client
	thirdPartyOnly bool
	titleGenerator *sessionNaming
	serviceTiers   *serviceTierSettings
	opencode       map[string]*grokClient
	grok           *grokClient
}

func newProviderClient(baseURL string, supplied *http.Client) *providerClient {
	provider := openai.New(baseURL, supplied)
	provider.ObserveWebSocket = func(ctx context.Context, payload []byte, headers, responseHeaders http.Header) openai.Observation {
		return capturer.BeginWebSocketAttempt(ctx, payload, headers, responseHeaders)
	}
	return &providerClient{openai: provider}
}

func (c *providerClient) forwardModels(ctx context.Context, headers http.Header, rawQuery string) (*http.Response, error) {
	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, criticalDiagnostic(err, "models_auth_headers", "the model catalog request is missing valid Codex Authorization or account headers", true)
	}
	upstream := http.Header{}
	upstream.Set("Authorization", authorization)
	upstream.Set(openai.AccountIDHeader, account)
	upstream.Set("Accept", "application/json")
	upstream.Set("Originator", codexresponses.ClientIdentity)
	upstream.Set("User-Agent", codexresponses.ClientIdentity)
	return c.openai.Models(ctx, upstream, rawQuery)
}

func (c *providerClient) forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
	var model struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, errors.New("invalid provider request")
	}
	if isOpenCodeModel(model.Model) {
		prefix, _, _ := strings.Cut(model.Model, ":")
		client := c.opencode[prefix]
		if client == nil {
			return nil, incompatibleRequest("opencode_not_configured", "OpenCode models require standalone mekugi with the provider's API key in the environment or Mekugi config.toml")
		}
		return client.forwardExecution(startCtx, responseCtx, body, headers)
	}
	if isGrokModel(model.Model) {
		if c.grok == nil {
			return nil, incompatibleRequest("grok_not_enabled", "Grok models require authenticated standalone mekugi or mekugi grok")
		}
		if c.grok.unprefixed != strings.HasPrefix(model.Model, "grok-") {
			return nil, incompatibleRequest("grok_model_namespace", "Use plain Grok model IDs in mekugi grok, and grok: model IDs in standalone mekugi")
		}
		return c.grok.forwardExecution(startCtx, responseCtx, body, headers)
	}
	if c.thirdPartyOnly {
		return nil, incompatibleRequest("third_party_model_required", "This launch supports only its authenticated third-party models; use mekugi codex for OpenAI models")
	}

	authorization, account, err := openai.Credentials(headers)
	if err != nil {
		return nil, err
	}
	upstream := http.Header{}
	upstream.Set("Authorization", authorization)
	upstream.Set(openai.AccountIDHeader, account)
	upstream.Set("Content-Type", "application/json")
	upstream.Set("Accept", "application/json, text/event-stream")
	upstream.Set("Originator", codexresponses.ClientIdentity)
	upstream.Set("User-Agent", codexresponses.ClientIdentity)
	codexresponses.ForwardHeaders(upstream, headers)
	if codexresponses.ValidCacheKey(cacheKey) {
		upstream[codexresponses.SessionIDHeader] = []string{cacheKey}
	}
	var websocket *openai.WebSocketRequest
	if c.openai.WebSocketsEnabled() {
		payload, handshake, stream, err := codexresponses.WebSocketRequest(body, upstream, cacheKey)
		if err != nil {
			return nil, err
		}
		websocket = &openai.WebSocketRequest{Payload: payload, Headers: handshake, Stream: stream}
	}
	return c.openai.Responses(startCtx, responseCtx, body, upstream, websocket)
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
		httptransport.RestorePrefix(response, prefix.Bytes(), io.MultiReader(probe, response.Body))
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
			if errors.Is(err, io.EOF) && !errors.Is(err, errDownstreamDisconnected) {
				return false, nil
			}
			return false, err
		}
	}
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

type responseTerminalState = responses.TerminalState

const (
	responseTerminalUnknown     = responses.TerminalUnknown
	responseTerminalInvalid     = responses.TerminalInvalid
	responseTerminalPending     = responses.TerminalPending
	responseTerminalCompleted   = responses.TerminalCompleted
	responseTerminalSteered     = responses.TerminalSteered
	responseTerminalFailed      = responses.TerminalFailed
	responseTerminalInterrupted = responses.TerminalInterrupted
)

func copyUpstreamBodyTransformed(writer io.Writer, response *http.Response, streamResponse bool, transformer responseTransformer, hooks *responseHooks) (responseTerminalState, error) {
	if streamResponse && hooks != nil && hooks.streamDiagnostics != nil {
		hooks.streamDiagnostics.responseStarted(response)
		if id := response.Header.Get("X-Request-Id"); safeDiagnosticIdentity(id) {
			hooks.streamDiagnostics.ProviderRequestID = id
		}
	}
	defer response.Body.Close()
	var (
		terminalState responseTerminalState
		err           error
	)
	if streamResponse {
		terminalState, err = copySSETransformed(writer, response.Body, transformer, hooks)
	} else {
		terminalState, err = copyJSONTransformed(writer, response.Body, transformer, hooks)
	}
	// The Grok adapter emits a failed terminal before closing its local pipe.
	// SSE copying stops at that terminal, so retrieve its producer error without
	// draining a provider stream or waiting after a downstream write failure.
	if err == nil && terminalState == responseTerminalFailed {
		if body, ok := response.Body.(*grokResponseBody); ok && body.terminalError != nil {
			err = body.terminalError()
		}
	}
	if err != nil {
		return terminalState, fmt.Errorf("copy upstream response: %w", err)
	}
	return terminalState, nil
}

func copyJSONTransformed(writer io.Writer, reader io.Reader, transformer responseTransformer, hooks *responseHooks) (responseTerminalState, error) {
	body, err := io.ReadAll(io.LimitReader(reader, upstreamJSONBufferBytes+1))
	if err != nil {
		return responseTerminalUnknown, err
	}
	if len(body) > upstreamJSONBufferBytes {
		return responseTerminalUnknown, fmt.Errorf("upstream JSON response exceeds the router buffer budget")
	}
	if hooks != nil {
		hooks.receivedAt = time.Now()
	}
	hooks.observe(body, false)
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

func copySSETransformed(writer io.Writer, reader io.Reader, transformer responseTransformer, hooks *responseHooks) (state responseTerminalState, resultErr error) {
	if hooks != nil {
		defer func() { hooks.streamDiagnostics.copyStopped(resultErr) }()
	}
	if hooks != nil {
		reader = diagnosticStreamReader{reader: reader, diagnostics: hooks.streamDiagnostics, receivedAt: &hooks.receivedAt}
	}
	buffered := bufio.NewReader(reader)

	if err := consumeOptionalUTF8BOM(buffered); err != nil {
		if hooks != nil {
			hooks.streamDiagnostics.readEnded(err)
		}
		return responseTerminalUnknown, err
	}
	event := []string{}
	terminalState := responseTerminalUnknown
	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			if line == "\n" || line == "\r\n" {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, line, transformer, hooks)
				terminalState = mergeResponseTerminalState(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				event = event[:0]
				if eventTerminalState.Terminal() {
					return terminalState, nil
				}
			} else {
				event = append(event, line)
			}
		}
		if err != nil {
			if hooks != nil {
				hooks.streamDiagnostics.readEnded(err)
			}
			if hooks != nil && hooks.streamDiagnostics != nil {
				hooks.streamDiagnostics.UnterminatedEvent = len(event) != 0
			}
			// A downstream disconnect may wrap EOF, but it is not a clean end
			// of the provider body. Preserve cancellation rather than replacing
			// it with an incomplete-call translation error during Finish.
			if errors.Is(err, io.EOF) && !errors.Is(err, errDownstreamDisconnected) {
				eventTerminalState, writeErr := writeSSEEvent(writer, event, "", transformer, hooks)
				terminalState = mergeResponseTerminalState(terminalState, eventTerminalState)
				if writeErr != nil {
					return terminalState, writeErr
				}
				if eventTerminalState.Terminal() {
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

func writeSSEEvent(writer io.Writer, lines []string, separator string, transformer responseTransformer, hooks *responseHooks) (responseTerminalState, error) {
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

	hooks.observe(payload, true)
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
		if !bytes.Equal(eventPayload, payload) && (len(visible) > 1 || transformedEnvelope.Type != originalEnvelope.Type) {
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
		if hooks != nil {
			var event struct {
				Type     string `json:"type"`
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal(payload, &event) == nil && event.Type == "response.created" {
				hooks.deliveredResponseID = event.Response.ID
			}
			if observeResponseTerminal(payload, true).Terminal() {
				hooks.deliveredTerminal = true
			}
		}
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

// Synthesized frames need the same event name and data framing as live SSE.
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

func isResponseAncillaryEvent(kind string) bool {
	return responses.Kind(kind).Ancillary()
}

func observeResponseTerminal(body []byte, streamEvent bool) responseTerminalState {
	return responses.ObserveTerminal(body, streamEvent)
}

func mergeResponseTerminalState(current, observed responseTerminalState) responseTerminalState {
	return responses.MergeTerminal(current, observed)
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
