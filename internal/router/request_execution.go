package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/responses"
)

// requestExecutor owns the stable services used by every attempt in a request,
// including router-generated journal continuations.
type requestExecutor struct {
	provider      responseProvider
	output        io.Writer
	issues        *CriticalErrors
	mekugiCalls   *mekugiProxy
	compactTokens *ctp2Codec
	mentor        *mentorHandoff
}

type requestContinuation struct {
	startCtx     context.Context
	executionCtx context.Context
	request      parsedResponsesRequest
}

type requestAttempt struct {
	executor     requestExecutor
	startCtx     context.Context
	executionCtx context.Context
	request      parsedResponsesRequest
	headers      http.Header
	sessionID    string

	journalOriginal    map[string]json.RawMessage
	journalStartWindow time.Duration
	hooks              *responseHooks
	finalization       requestFinalization
	debug              *debugOutput
	debugID            string
	started            time.Time
	trace              featureUsageTrace
	commentaryObserved bool

	metadata      codexTurnMetadata
	metadataValid bool
	threadID      string
	notices       *criticalErrorTransform
	handoff       *mentorRequest
	prewarm       bool

	mekugiTransform        *mekugiResponseTransform
	compactTokens          *ctp2Codec
	syntheticJournalFinish bool
	bridge                 *subagentBridge
	compactTransform       *ctp2ResponseTransform
	forwardBody            []byte
	usageTracker           *threadUsageObservation

	response           *http.Response
	streamResponse     bool
	responseTransform  responseTransformer
	finishUsage        bool
	releaseDelivery    bool
	continuationCancel context.CancelFunc
}

func (e requestExecutor) execute(
	startCtx context.Context,
	executionCtx context.Context,
	request parsedResponsesRequest,
	headers http.Header,
	sessionID string,
) (requestErr error) {
	var attempts []*requestAttempt
	defer func() {
		for index := len(attempts) - 1; index >= 0; index-- {
			requestErr = attempts[index].finish(requestErr)
		}
	}()

	for {
		attempt := newRequestAttempt(e, startCtx, executionCtx, request, headers, sessionID)
		attempts = append(attempts, attempt)
		continuation, err := attempt.run()
		if err != nil {
			return err
		}
		if continuation == nil {
			return nil
		}
		startCtx = continuation.startCtx
		executionCtx = continuation.executionCtx
		request = continuation.request
	}
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
	hooks := &responseHooks{}
	if debug != nil {
		hooks.streamDiagnostics = &streamDiagnostics{}
	}
	if exchange, ok := executor.provider.(*webSocketExchange); ok {
		exchange.streamDiagnostics = hooks.streamDiagnostics
	}
	trace := featureUsageTrace{
		debug: debug, requestID: debugID,
		threadID: codexThreadID(headers), sessionID: sessionID,
	}
	if debug != nil {
		trace.summary = &featureUsageSummary{counts: make(map[string]uint64)}
	}
	finalization := requestFinalization{failurePhase: requestFailurePrepare}
	if sessionID != "" {
		finalization.sessionID = sessionID
	}
	return &requestAttempt{
		executor: executor, startCtx: startCtx, executionCtx: executionCtx,
		request: request, headers: headers, sessionID: sessionID,
		journalOriginal:    maps.Clone(request.fields),
		journalStartWindow: journalRequestWindow(startCtx),
		hooks:              hooks, finalization: finalization,
		debug: debug, debugID: debugID, started: time.Now(), trace: trace,
		compactTokens: executor.compactTokens,
	}
}

func (a *requestAttempt) run() (*requestContinuation, error) {
	if err := a.prepare(); err != nil {
		return nil, err
	}
	if err := a.prepareWire(); err != nil {
		return nil, err
	}
	if err := a.forward(); err != nil {
		return nil, err
	}
	if err := a.prepareResponse(); err != nil {
		return nil, err
	}
	return a.deliver()
}

func (a *requestAttempt) prepare() error {
	if err := a.startCtx.Err(); err != nil {
		return fmt.Errorf("prepare request: %w", withRequestStartCause(a.startCtx, err))
	}
	a.metadata, a.metadataValid = decodeCodexTurnMetadata(a.headers)
	if a.metadataValid {
		capturer.ObserveRequestKind(a.startCtx, string(a.metadata.RequestKind))
	}
	a.threadID = codexThreadID(a.headers)
	if a.executor.mekugiCalls != nil && a.metadataValid && !a.metadata.activityIdentityInvalid &&
		(a.metadata.ThreadID == "" || a.metadata.ThreadID == a.threadID) {
		a.finalization.observeCriticalNotice = func(source, text string) {
			a.executor.mekugiCalls.activity.collect(a.threadID, source, "error", text)
		}
	}
	a.request.filterInput(func(map[string]json.RawMessage) {
		a.executor.issues.stripInput(&a.request, a.sessionID)
	})
	a.notices = a.executor.issues.transform(a.sessionID, a.metadata.SubagentKind != "")
	handoff, err := a.executor.mentor.prepare(a.headers, a.metadata, a.metadataValid, &a.request)
	if err != nil {
		return fmt.Errorf("prepare Mentor Handoff: %w", err)
	}
	a.handoff = handoff
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.history != nil {
		if exchange.automatic {
			parent := exchange.history.parent
			if parent == nil || parent.providerModel == "" {
				return errors.New("automatic WebSocket successor has no retained provider model")
			}
			a.request.fields["model"] = mustMarshalJSON(parent.providerModel)
			if len(parent.providerReasoning) == 0 {
				delete(a.request.fields, "reasoning")
			} else {
				a.request.fields["reasoning"] = parent.providerReasoning
			}
		}
		exchange.history.providerModel = a.request.model()
		exchange.history.providerReasoning = a.request.fields["reasoning"]
	}
	if a.handoff != nil {
		a.hooks.output = &a.handoff.observation
	}
	a.hooks.onFinished = func(result requestCompletion) {
		if a.handoff == nil {
			return
		}
		progress := a.handoff.record(result)
		if progress.transitioned && a.mekugiTransform != nil {
			broker := a.executor.mekugiCalls.commentary
			token := broker.subscribeThread(a.mekugiTransform.historySessionID, a.threadID, a.mekugiTransform.commentaryAuthor)
			broker.publish(token, "Mentor handoff complete.", false)
		}
	}

	// Only the WebSocket provider guarantees non-generating warmup for every
	// supported model. HTTP requests retain ordinary preparation checks.
	_, webSocketRequest := a.executor.provider.(*webSocketExchange)
	a.prewarm = webSocketRequest && a.metadataValid &&
		a.metadata.RequestKind == responses.Prewarm && string(a.request.fields["generate"]) == "false"
	if a.prewarm {
		a.compactTokens = nil
	}
	if a.executor.mekugiCalls != nil && !a.prewarm {
		a.mekugiTransform, err = a.executor.mekugiCalls.prepareRequest(
			a.startCtx,
			&a.request,
			a.sessionID,
			codexThreadID(a.headers),
			a.metadata,
			a.metadataValid,
		)
		if err != nil {
			return fmt.Errorf("prepare mekugi response proxy: %w", err)
		}
		// Compaction and auxiliary structured turns do not receive the
		// Mekugi instructions needed to decode CTP text.
		if a.mekugiTransform == nil {
			a.compactTokens = nil
		}
	}
	if a.executor.mekugiCalls != nil {
		workspace, usable := usableRoutingDirectory(a.metadata.Directories)
		if a.mekugiTransform != nil {
			workspace, usable = a.mekugiTransform.directory, true
		}
		if usable {
			a.notices.retain(a.startCtx, a.executor.mekugiCalls.replayStore, workspace)
		} else {
			// Without a canonical namespace the notice cannot be stripped from a
			// later ordinary turn, so leave it queued instead of emitting it.
			a.notices.suppress()
		}
	}
	if a.mekugiTransform != nil && a.metadataValid {
		a.executor.mekugiCalls.autoLiveDiff.observe(a.mekugiTransform.directory, a.mekugiTransform.threadID, a.metadata)
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.featureTrace = a.trace
		a.commentaryObserved = true
	}
	a.syntheticJournalFinish = a.mekugiTransform != nil && a.mekugiTransform.shellFinishRequested
	// A shell finish is already terminal for this attempt. Preserve normal
	// response transformation and journal delivery without provider generation.
	if a.syntheticJournalFinish {
		a.handoff = nil
		a.hooks.output = nil
		a.mekugiTransform.journalTerminal = true
		a.compactTokens = nil
	}
	return nil
}

func (a *requestAttempt) prepareWire() error {
	var err error
	bridgeProvider := a.executor.provider
	if exchange, ok := a.executor.provider.(*webSocketExchange); ok {
		bridgeProvider = exchange.session.provider
	}
	client, _ := bridgeProvider.(*providerClient)
	grokEnabled := client != nil && client.grok != nil
	if !a.syntheticJournalFinish && (a.mekugiTransform != nil || grokEnabled) {
		a.bridge, err = prepareSubagentBridge(&a.request, grokEnabled)
		if err != nil {
			return fmt.Errorf("prepare collaboration bridge: %w", err)
		}
	}
	nativeBody, err := a.request.wireBody(a.request.fields)
	if err != nil {
		return fmt.Errorf("encode native Responses request: %w", err)
	}
	a.compactTransform, a.forwardBody, err = a.compactTokens.prepareRequest(&a.request, nativeBody)
	if err != nil {
		return fmt.Errorf("prepare compact token protocol: %w", err)
	}
	if a.forwardBody == nil {
		a.forwardBody = nativeBody
	}
	if !a.syntheticJournalFinish {
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
	}
	nativeWire, err := a.request.incrementalBody(nativeBody)
	if err != nil {
		return err
	}
	if !a.syntheticJournalFinish {
		if exchange, ok := a.executor.provider.(*webSocketExchange); ok && exchange.automatic {
			capturer.ObserveNativeRequest(a.startCtx, nil)
		} else {
			capturer.ObserveNativeRequest(a.startCtx, nativeWire)
		}
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
	if !a.prewarm && !a.syntheticJournalFinish {
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
	forwardProvider := a.executor.provider
	if a.syntheticJournalFinish {
		forwardProvider = journalFinishResponseProvider{stream: a.request.streamResponse}
	}
	a.response, err = forwardProvider.forwardExecution(
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
	// Definite HTTP rejections did not admit inference. Transport failures and
	// accepted requests may still have consumed tokens without a usable terminal.
	if !rejectedUpgrade && (a.response == nil ||
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices) {
		a.finishUsage = true
	}
	if err != nil {
		err = withRequestStartCause(a.startCtx, err)
		return fmt.Errorf("execute request: %w", forwardCriticalDiagnostic(err))
	}
	a.finalization.upstreamStatusCode = a.response.StatusCode
	a.finalization.failurePhase = requestFailureInspectResponse
	if a.mekugiTransform != nil {
		a.mekugiTransform.ctx = a.executionCtx
	}
	a.streamResponse, err = prepareUpstreamBody(a.response, a.request.streamResponse)
	if err != nil {
		a.response.Body.Close()
		return fmt.Errorf("execute request: inspect upstream response: %w", err)
	}
	a.releaseDelivery = true
	return nil
}

func (a *requestAttempt) prepareResponse() error {
	if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
		if a.compactTransform != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.compactTransform)
		}
		if a.bridge != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.bridge)
		}
		if a.mekugiTransform != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.mekugiTransform)
		}
		if a.notices != nil {
			a.responseTransform = composeResponseTransformers(a.responseTransform, a.notices)
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
		if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
			if a.mekugiTransform != nil {
				a.mekugiTransform.observeResponseUsage(counts)
			} else {
				a.usageTracker.observe(counts)
			}
		}
		capturer.ObserveProviderUsage(a.executionCtx, capturer.ProviderUsage{
			EvidenceComplete: new(!counts.Incomplete && !counts.Inconsistent),
			InputTokens:      counts.InputTokens,
			CachedTokens:     counts.InputTokens - counts.UncachedInputTokens,
			OutputTokens:     counts.OutputTokens,
			ReasoningTokens:  counts.ReasoningTokens,
		})
	}
	if a.response.StatusCode < http.StatusOK || a.response.StatusCode >= http.StatusMultipleChoices {
		a.hooks.output = nil
	}
	return nil
}

func (a *requestAttempt) deliver() (*requestContinuation, error) {
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
			return nil, fmt.Errorf("execute request: %w", err)
		}
		stagedBody = staged.Bytes()
	}
	if !a.streamResponse && a.mekugiTransform != nil && a.mekugiTransform.journalContinue {
		return a.continueJournal()
	}
	if !a.streamResponse &&
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices &&
		!acceptsResponseEnd(a.finalization.upstreamTerminalState) {
		a.finalization.failurePhase = requestFailureTerminalValidation
		return nil, fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
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
				return nil, fmt.Errorf("execute request: flush response headers: %w", err)
			}
		}
	}
	if stagedBody != nil {
		a.finalization.failurePhase = requestFailureWriteResponse
		if _, err := a.executor.output.Write(stagedBody); err != nil {
			return nil, fmt.Errorf("execute request: copy upstream response: %w", err)
		}
		confirmResponseDelivery(a.responseTransform, stagedBody)
		releaseResponseDelivery(a.responseTransform)
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
			return nil, fmt.Errorf("execute request: %w", err)
		}
	}
	if a.streamResponse && a.mekugiTransform != nil && a.mekugiTransform.journalContinue {
		return a.continueJournal()
	}
	if a.streamResponse &&
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices &&
		!acceptsResponseEnd(a.finalization.upstreamTerminalState) {
		a.finalization.failurePhase = requestFailureTerminalValidation
		return nil, fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
	}
	a.finalization.failurePhase = ""
	switch {
	case a.response.StatusCode < http.StatusOK || a.response.StatusCode >= http.StatusMultipleChoices:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	case a.finalization.upstreamTerminalState == responseTerminalCompleted ||
		a.finalization.upstreamTerminalState == responseTerminalSteered:
		a.finalization.observation.outcome = requestOutcomeCompleted
		a.hooks.finish(a.finalization.completion())
	case a.finalization.upstreamTerminalState == responseTerminalFailed:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	default:
		a.finalization.observation.outcome = requestOutcomeFailed
		a.finalization.failurePhase = requestFailureTerminalValidation
	}
	return nil, nil
}

func (a *requestAttempt) continueJournal() (*requestContinuation, error) {
	next, err := nextJournalRequest(a.journalOriginal, a.mekugiTransform)
	if err != nil {
		return nil, err
	}
	nextCtx, err := continueJournalContext(a.executionCtx, a.mekugiTransform)
	if err != nil {
		return nil, err
	}
	a.mekugiTransform.Close()
	resetJournalExchange(a.executor.provider)
	start, cancel := context.WithTimeout(nextCtx, a.journalStartWindow)
	a.continuationCancel = cancel
	a.finalization.observation.outcome = requestOutcomeCompleted
	a.finalization.failurePhase = ""
	// Close this completed attempt before its successor can publish cumulative
	// usage or journal output. finish repeats these idempotent operations after
	// the continuation chain returns, matching ordinary attempt cleanup.
	a.hooks.finish(a.finalization.completion())
	a.usageTracker.finish()
	return &requestContinuation{startCtx: start, executionCtx: nextCtx, request: next}, nil
}

func (a *requestAttempt) finish(requestErr error) error {
	if a.continuationCancel != nil {
		a.continuationCancel()
	}
	if a.releaseDelivery && a.mekugiTransform != nil {
		a.mekugiTransform.ReleaseDelivery()
	}
	if a.finishUsage {
		a.usageTracker.finish()
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.Close()
	}
	if a.notices != nil {
		a.notices.finish(requestErr == nil)
	}

	requestErr = errors.Join(
		requestErr,
		a.finalization.finish(a.executionCtx, requestErr, a.executor.output, a.executor.issues),
	)
	a.hooks.finish(a.finalization.completion())
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
	a.trace.finish(
		a.commentaryObserved,
		requestErr == nil &&
			a.finalization.upstreamStatusCode >= http.StatusOK &&
			a.finalization.upstreamStatusCode < http.StatusMultipleChoices,
	)
	if a.hooks.streamDiagnostics != nil && a.hooks.streamDiagnostics.CopyStop != "" {
		fields["response_stream"] = a.hooks.streamDiagnostics.snapshot()
	}
	if a.finalization.diagnosticReference != "" {
		fields["diagnostic_reference"] = a.finalization.diagnosticReference
		fields["diagnostic_code"] = a.finalization.diagnosticCode
	}
	a.debug.event(fields)
	return requestErr
}

func acceptsResponseEnd(state responseTerminalState) bool {
	return state.Terminal()
}
