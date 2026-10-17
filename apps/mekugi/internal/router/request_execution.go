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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/usage/accounting"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// requestExecutor owns the services used to execute a request.
type requestExecutor struct {
	titleGenerator *sessionNaming
	serviceTiers   *inferencegateway.Tiers
	provider       inferencegateway.Forwarder
	client         *inferencegateway.Client
	exchange       *inferencegateway.Exchange
	output         io.Writer
	issues         *CriticalErrors
	mekugiCalls    *mekugiProxy
}

type requestAttempt struct {
	executor     requestExecutor
	startCtx     context.Context
	executionCtx context.Context
	request      codexresponses.Request
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
	bridge          codexresponses.Transformer
	forwardBody     []byte
	usageTracker    *accounting.Observation
	providerStarted time.Time

	response          *inferencegateway.Response
	streamResponse    bool
	responseTransform codexresponses.Transformer
	finishUsage       bool
}

func (e requestExecutor) executeWebSocket(startCtx, executionCtx context.Context, request codexresponses.Request, headers http.Header, exchange *inferencegateway.Exchange, output io.Writer) error {
	e.provider, e.exchange, e.output = exchange, exchange, output
	return e.execute(startCtx, executionCtx, request, headers, routingSessionID(headers, request))
}

func (e requestExecutor) execute(
	startCtx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
) (requestErr error) {
	if e.issues == nil {
		e.issues = NewCriticalErrors(nil)
	}
	attempt := newRequestAttempt(e, startCtx, executionCtx, request, headers, sessionID)
	defer func() { requestErr = attempt.finish(requestErr) }()
	return attempt.run()
}

func newRequestAttempt(
	executor requestExecutor,
	startCtx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
) *requestAttempt {
	debug, debugID := debugRequest(startCtx)
	hooks := &responseHooks{streamDiagnostics: transportcapture.NewStream(startCtx)}
	if exchange := executor.exchange; exchange != nil {
		exchange.StreamDiagnostics = hooks.streamDiagnostics
		exchange.ReadEnd = func(err error, origin string) { recordStreamReadEnd(hooks.streamDiagnostics, err, origin) }
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
		return fmt.Errorf("prepare request: %w", inferencegateway.WithRequestStartCause(a.startCtx, err))
	}
	a.hooks.onProviderFailure = func(payload []byte, stream bool) {
		if !stream && a.response.StatusCode >= http.StatusBadRequest {
			a.finalization.providerFailure = inferencegateway.NewProviderHTTPError("Upstream response", a.response.StatusCode, payload)
		} else {
			a.finalization.providerFailure = inferencegateway.TerminalProviderError(payload, stream)
		}
	}
	a.metadata, a.metadataValid = decodeCodexTurnMetadata(a.headers)
	if a.metadataValid {
		transportcapture.ObserveRequestKind(a.startCtx, string(a.metadata.RequestKind))
	}
	a.threadID = codexThreadID(a.headers)
	if a.metadataValid && a.metadata.RequestKind == responses.Turn {
		a.executor.titleGenerator.observeInput(a.threadID, a.request.Fields["input"])
	}
	a.finalization.threadID = a.threadID
	a.finalization.turnID = a.metadata.TurnID
	if a.executor.mekugiCalls != nil && a.metadataValid && a.metadata.RequestKind == responses.Compaction {
		if err := validateMekugiCompactionRequest(&a.request, a.metadata); err != nil {
			return err
		}
		if a.executor.mekugiCalls.isBTWThread(a.threadID) {
			return inferencegateway.IncompatibleRequest("btw_compaction_required", conversationmodel.SideCompactionRequired)
		}
	}
	if exchange := a.executor.exchange; exchange != nil && exchange.State != nil {
		if err := exchange.State.RestoreProviderModel(&a.request); err != nil {
			return err
		}
	}
	// Terra remains a selectable legacy Codex model, but all provider requests
	// use Sol. Do this after automatic-continuation selection.
	if model := inferencegateway.RoutedModel(a.request.Model()); model != a.request.Model() {
		a.request.Fields["model"] = responses.MustMarshalJSON(model)
	}
	if exchange := a.executor.exchange; exchange != nil && exchange.State != nil {
		exchange.State.RememberProviderModel(a.request)
	}
	requestedTier := responses.StringField(a.request.Fields, "service_tier")
	if tier := a.executor.serviceTiers.Effective(a.request.Model(), requestedTier, a.threadID); tier != requestedTier {
		a.request.Fields["service_tier"] = responses.MustMarshalJSON(tier)
	}

	if a.executor.mekugiCalls != nil {
		if guidance := a.executor.mekugiCalls.registry.Guidance; guidance != nil {
			if err := projectRequestInstructions(&a.request, guidance.BeforePreparation); err != nil {
				return err
			}
		}
	}

	// Only the WebSocket provider guarantees non-generating warmup for every
	// supported model. HTTP requests retain ordinary preparation checks.
	webSocketRequest := a.executor.exchange != nil
	a.prewarm = webSocketRequest && a.metadataValid &&
		a.metadata.RequestKind == responses.Prewarm && string(a.request.Fields["generate"]) == "false"
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
		auto := a.executor.mekugiCalls.review.Preview()
		observeReview(a.executor.mekugiCalls.review, a.mekugiTransform.directory, a.mekugiTransform.threadID, a.metadata)
		exchange := a.executor.exchange
		if exchange == nil || !exchange.State.Automatic() {
			beginLiveDiffTurn(auto, a.mekugiTransform.directory, a.mekugiTransform.threadID, a.metadata)
		}
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.toolTrace = a.trace
	}
	return nil
}

func (a *requestAttempt) prepareWire() error {
	var err error
	if !grok.IsModel(a.request.Model()) && !opencode.IsModel(a.request.Model()) {
		programs, err := standardCyberAccess(a.request.Fields["access_programs"])
		if err != nil {
			return err
		}
		a.request.Fields["access_programs"] = programs
	}
	var guidance codexresponses.CollaborationGuidance
	var providerGuidance bool
	if a.executor.client != nil {
		guidance, providerGuidance = a.executor.client.CollaborationGuidance()
	}
	// Compaction keeps request-wide projections but never gains collaboration
	// tools or their instructions.
	compaction := a.metadataValid && a.metadata.RequestKind == responses.Compaction
	if !compaction && (a.mekugiTransform != nil || a.prewarm && a.executor.mekugiCalls != nil || providerGuidance) {
		a.bridge, err = codexresponses.PrepareCollaboration(&a.request, guidance)
		if err != nil {
			return fmt.Errorf("prepare collaboration bridge: %w", err)
		}
	}
	attachmentsProjected := projectFileAttachments(&a.request)
	// Match attachment sources before compact skill rewriting drops native paths.
	if a.executor.mekugiCalls != nil {
		if guidance := a.executor.mekugiCalls.registry.Guidance; guidance != nil {
			if err := projectRequestInstructions(&a.request, func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
				return guidance.AfterAttachments(input, a.executor.mekugiCalls.skillsManager)
			}); err != nil {
				return err
			}
		}
	}
	nativeBody, err := a.request.WireBody(a.request.Fields)
	if err != nil {
		return fmt.Errorf("encode native Responses request: %w", err)
	}
	if attachmentsProjected && len(nativeBody) > codexresponses.MaxRequestBytes {
		return fmt.Errorf("request with file attachments exceeds the %d-byte request limit; reduce attachments or conversation context", codexresponses.MaxRequestBytes)
	}
	a.forwardBody = nativeBody
	if exchange := a.executor.exchange; exchange != nil {
		started := time.Now()
		reason, err := exchange.State.Reconcile(&a.request, !inferencegateway.IsThirdPartyModel(a.request.Model()))
		if err != nil {
			return inferencegateway.ProtocolError(err)
		}
		if a.debug != nil {
			a.debug.Event(map[string]any{
				"event": "provider_history_reconciliation", "request_id": a.debugID,
				"reason": reason, "reused_input_items": a.request.CachedInput,
				"duration_us": time.Since(started).Microseconds(),
			})
		}
	}
	nativeWire, err := a.request.IncrementalBody(nativeBody, !inferencegateway.IsThirdPartyModel(a.request.Model()))
	if err != nil {
		return err
	}
	if exchange := a.executor.exchange; exchange != nil && exchange.State.Automatic() {
		transportcapture.ObserveProjectedRequest(a.startCtx, nil)
	} else {
		transportcapture.ObserveProjectedRequest(a.startCtx, nativeWire)
	}

	return nil
}

func (a *requestAttempt) forward() error {
	projectedBody := a.forwardBody
	a.finalization.failurePhase = requestFailureForward
	cacheKey := a.request.PromptCacheKey()
	if cacheKey == "" {
		cacheKey = a.sessionID
	}
	var err error
	a.forwardBody, err = a.request.IncrementalBody(a.forwardBody, !inferencegateway.IsThirdPartyModel(a.request.Model()))
	if err != nil {
		return err
	}
	debugWire := a.forwardBody
	if exchange := a.executor.exchange; exchange != nil && exchange.State.Automatic() {
		debugWire = nil
	}
	a.debug.instructions(projectedBody, debugWire, a.headers, a.sessionID, a.debugID, a.request.CachedInput)
	if !a.prewarm {
		if a.mekugiTransform != nil {
			a.usageTracker = a.mekugiTransform.usageTracker
		} else if a.executor.mekugiCalls != nil && a.metadataValid {
			a.usageTracker = a.executor.mekugiCalls.usage.Observation(
				a.threadID,
				a.metadata.ThreadID,
				a.request.Model(),
				codexresponses.UsageServiceTier(a.request.Fields["service_tier"]),
			)
		}
	}
	if a.usageTracker != nil {
		a.usageTracker.Reasoning = a.request.ReasoningEffort()
		a.usageTracker.Begin()
	} else {
		a.providerStarted = time.Now()
	}
	a.response, err = a.executor.provider.ForwardExecution(
		a.startCtx,
		a.executionCtx,
		a.forwardBody,
		a.headers,
		cacheKey,
	)
	rejection, rejectedUpgrade := errors.AsType[*inferencegateway.WebSocketStatusError](err)
	if rejectedUpgrade {
		a.finalization.upstreamStatusCode = rejection.Status
	}
	providerRejection, rejectedProvider := errors.AsType[*inferencegateway.ProviderHTTPError](err)
	if rejectedProvider {
		a.finalization.upstreamStatusCode = providerRejection.Status
	}
	// Definite HTTP rejections did not admit inference. Transport failures and
	// accepted requests may still have consumed tokens without a usable terminal.
	if !rejectedUpgrade && !rejectedProvider && (a.response == nil ||
		a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices) {
		a.finishUsage = true
	}
	if err != nil {
		err = inferencegateway.WithRequestStartCause(a.startCtx, err)
		return fmt.Errorf("execute request: %w", forwardCriticalDiagnostic(err))
	}
	if a.usageTracker != nil {
		a.usageTracker.Price = pinnedOpenCodePrice(a.response.Price)
	}
	a.hooks.transport = a.response.Transport
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
	a.streamResponse, err = codexresponses.PrepareBody(a.response.Response, a.request.Stream)
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
			a.responseTransform = codexresponses.Compose(a.responseTransform, a.bridge)
		}
		if a.mekugiTransform != nil {
			a.responseTransform = codexresponses.Compose(a.responseTransform, a.mekugiTransform)
		}
	}
	if a.mekugiTransform != nil && a.responseTransform == nil {
		a.finalization.failurePhase = requestFailureTransform
		if err := a.mekugiTransform.Finish(false); err != nil {
			a.response.Body.Close()
			return fmt.Errorf("record mekugi request overhead: %w", err)
		}
	}
	a.hooks.onUsage = func(counts inference.Usage) {
		a.finalization.observation.usageCounts = counts
		a.finalization.observation.usageObserved = true
		if a.usageTracker != nil {
			a.usageTracker.ReceivedAt = a.hooks.receivedAt
		}
		if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
			if a.mekugiTransform != nil {
				a.mekugiTransform.observeResponseUsage(counts)
			} else {
				a.usageTracker.Observe(counts)
			}
		}
		throughput := accounting.MeasureThroughput(counts, a.providerStarted, a.hooks.receivedAt)
		if a.usageTracker != nil {
			throughput = a.usageTracker.Throughput
		}
		transportcapture.ObserveProviderUsage(a.executionCtx, transportcapture.ProviderUsage{
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
			a.response.Response,
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
		codexresponses.ConfirmDelivery(a.responseTransform, stagedBody)
	} else {
		var err error
		a.finalization.failurePhase = requestFailureInspectResponse
		a.finalization.upstreamTerminalState, err = copyUpstreamBodyTransformed(
			a.executor.output,
			a.response.Response,
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
	case a.finalization.upstreamTerminalState == responses.TerminalCompleted ||
		a.finalization.upstreamTerminalState == responses.TerminalSteered ||
		a.finalization.upstreamTerminalState == responses.TerminalInterrupted:
		a.finalization.observation.outcome = requestOutcomeCompleted
	case a.finalization.upstreamTerminalState == responses.TerminalFailed:
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
		a.finalization.upstreamTerminalState == responses.TerminalCompleted {
		a.executor.titleGenerator.completed(a.threadID, a.headers)
	}
}

func (a *requestAttempt) finish(requestErr error) error {
	if a.finishUsage {
		a.usageTracker.Finish()
	}
	if a.mekugiTransform != nil {
		a.mekugiTransform.Close()
	}

	a.finalization.finish(a.executionCtx, requestErr, a.executor.output, a.executor.issues)
	_, compatibilityFault := errors.AsType[*inferencegateway.CompatibilityError](requestErr)
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
		deliveryErr := codexresponses.WriteEvent(a.executor.output, payload)
		requestErr = errors.Join(requestErr, deliveryErr)
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
	if captureID, sequence := transportcapture.RequestCorrelation(a.startCtx); captureID != "" {
		fields["capture_id"], fields["request_sequence"] = captureID, sequence
	}
	if cause := requestCancellationCause(a.executionCtx, requestErr); cause != "" {
		fields["cancellation_cause"] = cause
	}
	if errors.Is(requestErr, httptransport.ErrStreamIdleTimeout) {
		fields["idle_timeout_observed"] = true
	}

	if a.hooks.streamDiagnostics != nil && a.hooks.streamDiagnostics.CopyStop != "" {
		fields["response_stream"] = a.hooks.streamDiagnostics.Snapshot()
	}
	if a.finalization.diagnosticReference != "" {
		fields["diagnostic_reference"] = a.finalization.diagnosticReference
		fields["diagnostic_code"] = a.finalization.diagnosticCode
		fields["error"] = a.finalization.diagnosticError
	}
	if a.debug != nil {
		a.debug.Event(fields)
	}
	return requestErr
}
