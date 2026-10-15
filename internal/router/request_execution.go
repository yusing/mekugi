package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

type responseProvider interface {
	forwardExecution(startCtx, responseCtx context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error)
}

// requestExecutor owns the services used to execute a request.
type requestExecutor struct {
	titleGenerator *sessionTitleGenerator
	serviceTiers   *serviceTierSettings
	provider       responseProvider
	output         io.Writer
	issues         *CriticalErrors
	mekugiCalls    *mekugiProxy
}

type requestAttempt struct {
	executor     requestExecutor
	startCtx     context.Context
	executionCtx context.Context
	request      parsedResponsesRequest
	headers      http.Header
	sessionID    string

	hooks        *responseHooks
	finalization requestFinalization
	debug        *debugOutput
	debugID      string
	started      time.Time
	trace        toolObservationTrace

	metadata      codexTurnMetadata
	metadataValid bool
	threadID      string
	prewarm       bool

	mekugiTransform *mekugiResponseTransform
	bridge          *subagentBridge
	forwardBody     []byte
	usageTracker    *threadUsageObservation
	providerStarted time.Time

	response          *http.Response
	streamResponse    bool
	responseTransform responseTransformer
	finishUsage       bool
}

func (e requestExecutor) execute(
	startCtx context.Context,
	executionCtx context.Context,
	request parsedResponsesRequest,
	headers http.Header,
	sessionID string,
) (requestErr error) {
	if e.issues == nil {
		e.issues = NewCriticalErrors()
	}
	attempt := newRequestAttempt(e, startCtx, executionCtx, request, headers, sessionID)
	defer func() { requestErr = attempt.finish(requestErr) }()
	return attempt.run()
}

func newRequestAttempt(
	executor requestExecutor,
	startCtx context.Context,
	executionCtx context.Context,
	request parsedResponsesRequest,
	headers http.Header,
	sessionID string,
) *requestAttempt {
	debug, debugID := debugRequest(startCtx)
	hooks := &responseHooks{streamDiagnostics: &streamDiagnostics{}}
	if exchange, ok := executor.provider.(*webSocketExchange); ok {
		exchange.streamDiagnostics = hooks.streamDiagnostics
	}
	trace := toolObservationTrace{
		debug: debug, requestID: debugID,
		threadID: codexThreadID(headers), sessionID: sessionID,
	}

	finalization := requestFinalization{failurePhase: requestFailurePrepare, streamDiagnostics: hooks.streamDiagnostics}
	if sessionID != "" {
		finalization.sessionID = sessionID
	}
	return &requestAttempt{
		executor: executor, startCtx: startCtx, executionCtx: executionCtx,
		request: request, headers: headers, sessionID: sessionID,
		hooks: hooks, finalization: finalization,
		debug: debug, debugID: debugID, started: time.Now(), trace: trace,
	}
}

func (a *requestAttempt) run() error {
	if err := a.prepare(); err != nil {
		return err
	}
	if err := a.prepareWire(); err != nil {
		return err
	}
	if err := a.forward(); err != nil {
		return err
	}
	if err := a.prepareResponse(); err != nil {
		return err
	}
	return a.deliver()
}

func (a *requestAttempt) prepare() error {
	if err := a.startCtx.Err(); err != nil {
		return fmt.Errorf("prepare request: %w", withRequestStartCause(a.startCtx, err))
	}
	a.hooks.onProviderFailure = func(payload []byte, stream bool) {
		if !stream && a.response.StatusCode >= http.StatusBadRequest {
			a.finalization.providerFailure = newProviderHTTPError("Upstream response", a.response.StatusCode, payload)
		} else {
			a.finalization.providerFailure = terminalProviderError(payload, stream, a.headers)
		}
	}
	a.metadata, a.metadataValid = decodeCodexTurnMetadata(a.headers)
	if a.metadataValid {
		capturer.ObserveRequestKind(a.startCtx, string(a.metadata.RequestKind))
	}
	a.threadID = codexThreadID(a.headers)
	if a.metadataValid && a.metadata.RequestKind == responses.Turn && a.executor.titleGenerator.needsPrompt(a.threadID) {
		a.executor.titleGenerator.observe(a.threadID, requestUserTextFromInput(a.request.fields["input"], "/root", true), a.headers, false)
	}
	a.finalization.threadID = a.threadID
	a.finalization.turnID = a.metadata.TurnID
	if a.executor.mekugiCalls != nil && a.metadataValid && a.metadata.RequestKind == responses.Compaction {
		if err := validateMekugiCompactionRequest(&a.request, a.metadata); err != nil {
			return err
		}
		if a.executor.mekugiCalls.isBTWThread(a.threadID) {
			return &requestCompatibilityError{code: "btw_compaction_required", message: btwCompactionRequired}
		}
	}
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.history != nil {
		if exchange.automatic {
			parent := exchange.history.parent
			if parent == nil || parent.providerModel == "" {
				return errors.New("automatic WebSocket successor has no retained provider model")
			}
			a.request.fields["model"] = responses.MustMarshalJSON(parent.providerModel)
			if len(parent.providerReasoning) == 0 {
				delete(a.request.fields, "reasoning")
			} else {
				a.request.fields["reasoning"] = parent.providerReasoning
			}
		}
	}
	// Terra remains a selectable legacy Codex model, but all provider requests
	// use Sol. Do this after automatic-continuation selection.
	if a.request.model() == "gpt-5.6-terra" {
		a.request.fields["model"] = responses.MustMarshalJSON("gpt-6-sol")
	}
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.history != nil {
		exchange.history.providerModel = a.request.model()
		exchange.history.providerReasoning = a.request.fields["reasoning"]
	}
	requestedTier := responses.StringField(a.request.fields, "service_tier")
	if tier := effectiveServiceTier(a.request.model(), requestedTier, a.executor.serviceTiers, a.threadID); tier != requestedTier {
		a.request.fields["service_tier"] = responses.MustMarshalJSON(tier)
	}

	if a.executor.mekugiCalls != nil {
		if err := stripRequestInstructionOmissions(&a.request); err != nil {
			return err
		}
	}

	// Only the WebSocket provider guarantees non-generating warmup for every
	// supported model. HTTP requests retain ordinary preparation checks.
	_, webSocketRequest := a.executor.provider.(*webSocketExchange)
	a.prewarm = webSocketRequest && a.metadataValid &&
		a.metadata.RequestKind == responses.Prewarm && string(a.request.fields["generate"]) == "false"
	if a.executor.mekugiCalls != nil {
		var err error
		a.mekugiTransform, err = a.executor.mekugiCalls.prepareModelRequest(
			a.startCtx,
			&a.request,
			a.sessionID,
			codexThreadID(a.headers),
			a.metadata,
			a.metadataValid,
			a.prewarm,
		)
		if err != nil {
			return fmt.Errorf("prepare mekugi response proxy: %w", err)
		}
	}
	if a.mekugiTransform != nil && a.metadataValid {
		auto := a.executor.mekugiCalls.autoLiveDiff
		auto.observe(a.mekugiTransform.directory, a.mekugiTransform.threadID, a.metadata)
		exchange, webSocket := a.executor.provider.(*webSocketExchange)
		if !webSocket || !exchange.automatic {
			auto.beginTurn(a.mekugiTransform.directory, a.mekugiTransform.threadID, a.metadata)
		}
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.toolTrace = a.trace
	}
	return nil
}

func (a *requestAttempt) prepareWire() error {
	var err error
	if !isGrokModel(a.request.model()) && !isOpenCodeModel(a.request.model()) {
		programs, err := standardCyberAccess(a.request.fields["access_programs"])
		if err != nil {
			return err
		}
		a.request.fields["access_programs"] = programs
	}
	bridgeProvider := a.executor.provider
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok {
		bridgeProvider = exchange.session.provider
	}
	client, _ := bridgeProvider.(*providerClient)
	grokEnabled := client != nil && client.grok != nil
	var openCodeModels []string
	if client != nil {
		for _, prefix := range []string{"opencode-go", "opencode-zen"} {
			if client.opencode[prefix] != nil {
				for _, model := range client.opencode[prefix].openCode.Models() {
					openCodeModels = append(openCodeModels, prefix+":"+model.ID)
				}
			}
		}
	}
	// Compaction keeps request-wide projections but never gains collaboration
	// tools or their instructions.
	compaction := a.metadataValid && a.metadata.RequestKind == responses.Compaction
	if !compaction && (a.mekugiTransform != nil || a.prewarm && a.executor.mekugiCalls != nil || grokEnabled || len(openCodeModels) > 0) {
		a.bridge, err = prepareSubagentBridge(&a.request, grokEnabled, grokEnabled && client.grok.unprefixed, openCodeModels...)
		if err != nil {
			return fmt.Errorf("prepare collaboration bridge: %w", err)
		}
	}
	attachmentsProjected := projectFileAttachments(&a.request)
	// Match attachment sources before compact skill rewriting drops native paths.
	if a.executor.mekugiCalls != nil {
		if err := rewriteRequestSelectedSkillInstructions(&a.request, a.executor.mekugiCalls.skillsManager); err != nil {
			return err
		}
	}
	nativeBody, err := a.request.wireBody(a.request.fields)
	if err != nil {
		return fmt.Errorf("encode native Responses request: %w", err)
	}
	if attachmentsProjected && len(nativeBody) > responsesRequestBufferBytes {
		return fmt.Errorf("request with file attachments exceeds the %d-byte request limit; reduce attachments or conversation context", responsesRequestBufferBytes)
	}
	a.forwardBody = nativeBody
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok {
		started := time.Now()
		if err := exchange.reconcileProviderHistory(&a.request, a.forwardBody); err != nil {
			return err
		}
		a.debug.event(map[string]any{
			"event": "provider_history_reconciliation", "request_id": a.debugID,
			"reason": exchange.reconciliationReason, "reused_input_items": a.request.cachedInput,
			"duration_us": time.Since(started).Microseconds(),
		})
	}
	nativeWire, err := a.request.incrementalBody(nativeBody)
	if err != nil {
		return err
	}
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.automatic {
		capturer.ObserveProjectedRequest(a.startCtx, nil)
	} else {
		capturer.ObserveProjectedRequest(a.startCtx, nativeWire)
	}

	return nil
}

func (a *requestAttempt) forward() error {
	projectedBody := a.forwardBody
	a.finalization.failurePhase = requestFailureForward
	cacheKey := a.request.promptCacheKey()
	if cacheKey == "" {
		cacheKey = a.sessionID
	}
	var err error
	a.forwardBody, err = a.request.incrementalBody(a.forwardBody)
	if err != nil {
		return err
	}
	debugWire := a.forwardBody
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.automatic {
		debugWire = nil
	}
	a.debug.instructions(projectedBody, debugWire, a.headers, a.sessionID, a.debugID, a.request.cachedInput)
	if !a.prewarm {
		if a.mekugiTransform != nil {
			a.usageTracker = a.mekugiTransform.usageTracker
		} else if a.executor.mekugiCalls != nil && a.metadataValid {
			a.usageTracker = a.executor.mekugiCalls.usage.observation(
				a.threadID,
				a.metadata.ThreadID,
				a.request.model(),
				usageServiceTier(a.request.fields["service_tier"]),
			)
		}
	}
	if a.usageTracker != nil {
		a.usageTracker.reasoning = a.request.reasoningEffort()
		a.usageTracker.begin()
	} else {
		a.providerStarted = time.Now()
	}
	a.response, err = a.executor.provider.forwardExecution(
		a.startCtx,
		a.executionCtx,
		a.forwardBody,
		a.headers,
		cacheKey,
	)
	rejection, rejectedUpgrade := errors.AsType[*webSocketStatusError](err)
	if rejectedUpgrade {
		a.finalization.upstreamStatusCode = rejection.status
	}
	providerRejection, rejectedProvider := errors.AsType[*providerHTTPError](err)
	if rejectedProvider {
		a.finalization.upstreamStatusCode = providerRejection.status
	}
	// Definite HTTP rejections did not admit inference. Transport failures and
	// accepted requests may still have consumed tokens without a usable terminal.
	if !rejectedUpgrade && !rejectedProvider && (a.response == nil ||
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices) {
		a.finishUsage = true
	}
	if err != nil {
		err = withRequestStartCause(a.startCtx, err)
		return fmt.Errorf("execute request: %w", forwardCriticalDiagnostic(err))
	}
	if body, ok := a.response.Body.(*grokResponseBody); ok && a.usageTracker != nil {
		a.usageTracker.openCodePrice = body.openCodePrice
	}
	a.finalization.upstreamStatusCode = a.response.StatusCode
	a.finalization.failurePhase = requestFailureInspectResponse
	if a.mekugiTransform != nil {
		// Drop the response-start deadline, not the prepared storage identity
		// and turn lease. Child changes share their parent's handle namespace.
		ctx := a.executionCtx
		for _, key := range []any{storageSessionKey{}, storageTurnKey{}} {
			if value := a.mekugiTransform.ctx.Value(key); value != nil {
				ctx = context.WithValue(ctx, key, value)
			}
		}
		a.mekugiTransform.ctx = ctx
	}
	a.streamResponse, err = prepareUpstreamBody(a.response, a.request.streamResponse)
	if err != nil {
		a.response.Body.Close()
		return fmt.Errorf("execute request: inspect upstream response: %w", err)
	}
	return nil
}

func (a *requestAttempt) prepareResponse() error {
	a.hooks.upstreamStatus = a.response.StatusCode
	if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
		if a.bridge != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.bridge)
		}
		if a.mekugiTransform != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.mekugiTransform)
		}
	}
	if a.mekugiTransform != nil && a.responseTransform == nil {
		a.finalization.failurePhase = requestFailureTransform
		if err := a.mekugiTransform.Finish(false); err != nil {
			a.response.Body.Close()
			return fmt.Errorf("record mekugi request overhead: %w", err)
		}
	}
	a.hooks.onUsage = func(counts tokenCounts) {
		a.finalization.observation.usageCounts = counts
		a.finalization.observation.usageObserved = true
		if a.usageTracker != nil {
			a.usageTracker.receivedAt = a.hooks.receivedAt
		}
		if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
			if a.mekugiTransform != nil {
				a.mekugiTransform.observeResponseUsage(counts)
			} else {
				a.usageTracker.observe(counts)
			}
		}
		throughput := measureOutputThroughput(counts, a.providerStarted, a.hooks.receivedAt)
		if a.usageTracker != nil {
			throughput = a.usageTracker.throughput
		}
		capturer.ObserveProviderUsage(a.executionCtx, capturer.ProviderUsage{
			OutputThroughput: throughput,
			EvidenceComplete: new(!counts.Incomplete && !counts.Inconsistent),
			InputTokens:      counts.InputTokens,
			CachedTokens:     counts.InputTokens - counts.UncachedInputTokens,
			OutputTokens:     counts.OutputTokens,
			ReasoningTokens:  counts.ReasoningTokens,
		})
	}
	return nil
}

func (a *requestAttempt) deliver() error {
	var stagedBody []byte
	if !a.streamResponse {
		var staged bytes.Buffer
		var err error
		a.finalization.upstreamTerminalState, err = copyUpstreamBodyTransformed(
			&staged,
			a.response,
			false,
			a.responseTransform,
			a.hooks,
		)
		if err != nil {
			a.finalization.classifyCopyError(err)
			return fmt.Errorf("execute request: %w", err)
		}
		stagedBody = staged.Bytes()
	}

	if !a.streamResponse &&
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices &&
		!a.finalization.upstreamTerminalState.Terminal() {
		a.finalization.failurePhase = requestFailureTerminalValidation
		return fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
	}
	if writer, ok := a.executor.output.(http.ResponseWriter); ok {
		for name, values := range a.response.Header {
			if http.CanonicalHeaderKey(name) != "Content-Length" {
				writer.Header()[name] = slices.Clone(values)
			}
		}
		writer.WriteHeader(a.response.StatusCode)
	}
	if a.streamResponse {
		if writer, ok := a.executor.output.(http.ResponseWriter); ok {
			a.finalization.failurePhase = requestFailureWriteResponse
			if err := http.NewResponseController(writer).Flush(); err != nil {
				a.response.Body.Close()
				return fmt.Errorf("execute request: flush response headers: %w", err)
			}
		}
	}
	if stagedBody != nil {
		a.finalization.failurePhase = requestFailureWriteResponse
		if _, err := a.executor.output.Write(stagedBody); err != nil {
			return fmt.Errorf("execute request: copy upstream response: %w", err)
		}
		confirmResponseDelivery(a.responseTransform, stagedBody)
	} else {
		var err error
		a.finalization.failurePhase = requestFailureInspectResponse
		a.finalization.upstreamTerminalState, err = copyUpstreamBodyTransformed(
			a.executor.output,
			a.response,
			a.streamResponse,
			a.responseTransform,
			a.hooks,
		)
		if err != nil {
			a.finalization.classifyCopyError(err)
			return fmt.Errorf("execute request: %w", err)
		}
	}
	a.observeTitleSuccess()

	if a.streamResponse &&
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices &&
		!a.finalization.upstreamTerminalState.Terminal() {
		a.finalization.failurePhase = requestFailureTerminalValidation
		return fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
	}
	a.finalization.failurePhase = ""
	switch {
	case a.response.StatusCode < http.StatusOK || a.response.StatusCode >= http.StatusMultipleChoices:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	case a.finalization.upstreamTerminalState == responseTerminalCompleted ||
		a.finalization.upstreamTerminalState == responseTerminalSteered ||
		a.finalization.upstreamTerminalState == responseTerminalInterrupted:
		a.finalization.observation.outcome = requestOutcomeCompleted
	case a.finalization.upstreamTerminalState == responseTerminalFailed:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	default:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	}
	return nil
}

func (a *requestAttempt) observeTitleSuccess() {
	if a.metadataValid && a.metadata.RequestKind == responses.Turn && !a.prewarm &&
		a.response.StatusCode >= 200 && a.response.StatusCode < 300 &&
		a.finalization.upstreamTerminalState == responseTerminalCompleted {
		a.executor.titleGenerator.observe(a.threadID, "", a.headers, true)
	}
}

func (a *requestAttempt) finish(requestErr error) error {
	if a.finishUsage {
		a.usageTracker.finish()
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.Close()
	}

	a.finalization.finish(a.executionCtx, requestErr, a.executor.output, a.executor.issues)
	_, compatibilityFault := errors.AsType[*requestCompatibilityError](requestErr)
	if (a.finalization.failurePhase == requestFailureTransform || a.finalization.failurePhase == requestFailurePrepare && compatibilityFault) &&
		a.hooks.deliveredResponseID != "" && !a.hooks.deliveredTerminal &&
		a.finalization.observation.outcome == requestOutcomeFailed {
		payload := responses.MustMarshalJSON(map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": a.hooks.deliveredResponseID, "status": "failed", "output": []any{},
				"error": map[string]any{"code": "invalid_prompt", "message": a.finalization.diagnosticMessage},
			},
		})
		_, deliveryErr := writeSSEEvent(a.executor.output, responseSSELines(payload, "\n"), "\n", nil, nil)
		requestErr = errors.Join(requestErr, deliveryErr)
		if deliveryErr == nil && a.executor.issues != nil {
			a.executor.issues.mu.Lock()
			if notice := a.finalization.diagnosticNotice; notice != nil {
				notice.delivered = notice.count
			}
			a.executor.issues.mu.Unlock()
		}
	}
	fields := map[string]any{
		"event": "request_complete", "request_id": a.debugID,
		"client_request_id": a.headers.Get("x-client-request-id"),
		"thread_id":         codexThreadID(a.headers),
		"session_id":        a.sessionID,
		"outcome":           a.finalization.observation.outcome.String(),
		"phase":             a.finalization.failurePhase,
		"upstream_status":   a.finalization.upstreamStatusCode,
		"duration_ms":       time.Since(a.started).Milliseconds(),
	}
	if captureID, sequence := capturer.RequestCorrelation(a.startCtx); captureID != "" {
		fields["capture_id"], fields["request_sequence"] = captureID, sequence
	}
	if cause := requestCancellationCause(a.executionCtx, requestErr); cause != "" {
		fields["cancellation_cause"] = cause
	}
	if errors.Is(requestErr, errUpstreamStreamIdleTimeout) {
		fields["idle_timeout_observed"] = true
	}

	if a.hooks.streamDiagnostics != nil && a.hooks.streamDiagnostics.CopyStop != "" {
		fields["response_stream"] = a.hooks.streamDiagnostics.snapshot()
	}
	if a.finalization.diagnosticReference != "" {
		fields["diagnostic_reference"] = a.finalization.diagnosticReference
		fields["diagnostic_code"] = a.finalization.diagnosticCode
		fields["error"] = a.finalization.diagnosticError
	}
	a.debug.event(fields)
	return requestErr
}
