// Source: apps/mekugi/internal/router/request_execution.go:1:583@b9679c93.
package requestexecution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/modules/usage/accounting"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// Executor runs attempts using borrowed application services. Each invocation
// keeps its original capture, usage, control and retained-publication owners.
type Executor struct {
	ThreadControl *control.Service
	Naming        Naming
	ServiceTiers  *inferencegateway.Tiers
	Provider      inferencegateway.Forwarder
	Client        *inferencegateway.Client
	Exchange      *inferencegateway.Exchange
	Output        io.Writer
	Issues        *Failures
	Observations  Preparer
	Guidance      *binding.Guidance
	SkillsManager bool
	CompactPrompt func() string
	CompactStore  func(context.Context, string, string, string) (string, error)
	Control       *sessioncontrol.Observations
	Usage         *accounting.Service
}

type requestAttempt struct {
	executor     Executor
	startCtx     context.Context
	executionCtx context.Context
	request      codexresponses.Request
	headers      http.Header
	sessionID    string

	hooks        *ResponseHooks
	finalization requestFinalization
	debug        *Debug
	debugID      string
	started      time.Time
	trace        Trace

	metadata       codexresponses.TurnMetadata
	metadataValid  bool
	approvalReview bool
	threadID       string
	prewarm        bool

	prepared        Prepared
	forwardBody     []byte
	usageTracker    *accounting.Observation
	providerStarted time.Time

	response          *inferencegateway.Response
	streamResponse    bool
	responseTransform codexresponses.Transformer
	finishUsage       bool
}

// ExecuteHTTP binds an admitted HTTP exchange to the shared request lifecycle.
func (e Executor) ExecuteHTTP(startCtx, executionCtx context.Context, request codexresponses.Request, headers http.Header, output io.Writer) error {
	e.Output = output
	return e.Execute(startCtx, executionCtx, request, headers, codexresponses.RoutingSessionID(headers, request))
}

func (e Executor) ExecuteWebSocket(startCtx, executionCtx context.Context, request codexresponses.Request, headers http.Header, exchange *inferencegateway.Exchange, output io.Writer) error {
	e.Provider, e.Exchange, e.Output = exchange, exchange, output
	return e.Execute(startCtx, executionCtx, request, headers, codexresponses.RoutingSessionID(headers, request))
}

func (e Executor) Execute(
	startCtx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
) (requestErr error) {
	if e.Issues == nil {
		e.Issues = NewFailures(nil)
	}
	attempt := newRequestAttempt(e, startCtx, executionCtx, request, headers, sessionID)
	defer func() { requestErr = attempt.finish(requestErr) }()
	return attempt.run()
}

func newRequestAttempt(
	executor Executor,
	startCtx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
) *requestAttempt {
	debug, debugID := DebugRequest(startCtx)
	hooks := &ResponseHooks{StreamDiagnostics: transportcapture.NewStream(startCtx)}
	if exchange := executor.Exchange; exchange != nil {
		exchange.StreamDiagnostics = hooks.StreamDiagnostics
		exchange.ReadEnd = func(err error, origin string) { recordStreamReadEnd(hooks.StreamDiagnostics, err, origin) }
	}
	trace := Trace{
		debug: debug, requestID: debugID,
		threadID: codexresponses.ThreadID(headers), sessionID: sessionID,
	}

	finalization := requestFinalization{failurePhase: requestFailurePrepare, streamDiagnostics: hooks.StreamDiagnostics}
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
	if handled, err := a.completeLocalWait(); handled || err != nil {
		return err
	}
	if local, ok := a.prepared.(interface{ CompactReload() jsontext.Value }); ok && len(local.CompactReload()) > 0 {
		var body bytes.Buffer
		item := local.CompactReload()
		id := "resp_mekugi_reload_" + rand.Text()
		for _, event := range []any{
			map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}},
		} {
			if err := codexresponses.WriteEvent(&body, responses.MustMarshalJSON(event)); err != nil {
				return err
			}
		}
		a.response = &inferencegateway.Response{Response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(&body)}}
		a.streamResponse, a.responseTransform = true, a.prepared
		a.prepared.BindExecution(a.executionCtx)
		return a.deliver()
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
	if !a.approvalReview && a.metadataValid && a.metadata.RequestKind == responses.Compaction && a.executor.CompactPrompt != nil && a.response.StatusCode >= 200 && a.response.StatusCode < 300 {
		return a.deliverCompactSummary()
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
	a.metadata, a.metadataValid = codexresponses.DecodeTurnMetadata(a.headers)
	a.approvalReview = codexresponses.ApprovalReview(a.headers, a.metadata)
	if a.metadataValid {
		transportcapture.ObserveRequestKind(a.startCtx, string(a.metadata.RequestKind))
	}
	a.threadID = codexresponses.ThreadID(a.headers)
	// Independent runtimes have root-shaped host metadata. Logical membership
	// supplies their observation identity without rewriting host wire facts.
	if !a.approvalReview && a.metadataValid && !a.metadata.ActivityIdentityInvalid &&
		(a.metadata.ThreadID == "" || a.metadata.ThreadID == a.threadID) && a.executor.ThreadControl != nil {
		if member, ok := a.executor.ThreadControl.Member(a.threadID); ok && member.Parent != "" && a.metadata.SubagentKind == "" {
			a.metadata.ParentThreadID, a.metadata.AgentName = member.Parent, member.Task
			a.metadata.SubagentKind = "thread_spawn"
		}
	}
	if !a.approvalReview && a.executor.Naming != nil && a.metadataValid && a.metadata.RequestKind == responses.Turn {
		a.executor.Naming.ObserveInput(a.threadID, a.request.Fields["input"])
	}
	a.finalization.threadID = a.threadID
	a.finalization.turnID = a.metadata.TurnID
	if !a.approvalReview && a.executor.Observations != nil && a.metadataValid && a.metadata.RequestKind == responses.Compaction {
		if err := codexresponses.ValidateCompaction(&a.request, a.metadata); err != nil {
			return err
		}
		if a.executor.Control.IsSideThread(a.threadID) {
			return inferencegateway.IncompatibleRequest("btw_compaction_required", conversationmodel.SideCompactionRequired)
		}
	}
	if exchange := a.executor.Exchange; exchange != nil && exchange.State != nil {
		if err := exchange.State.RestoreProviderModel(&a.request); err != nil {
			return err
		}
	}
	if exchange := a.executor.Exchange; exchange != nil && exchange.State != nil {
		exchange.State.RememberProviderModel(a.request)
	}
	requestedTier := responses.StringField(a.request.Fields, "service_tier")
	if tier := a.executor.ServiceTiers.Effective(a.request.Model(), requestedTier, a.threadID); tier != requestedTier {
		a.request.Fields["service_tier"] = responses.MustMarshalJSON(tier)
	}

	if !a.approvalReview && a.executor.Observations != nil {
		if guidance := a.executor.Guidance; guidance != nil {
			if err := ProjectInstructions(&a.request, guidance.BeforePreparation); err != nil {
				return err
			}
		}
	}

	// Only the WebSocket provider guarantees non-generating warmup for every
	// supported model. HTTP requests retain ordinary preparation checks.
	webSocketRequest := a.executor.Exchange != nil
	a.prewarm = webSocketRequest && a.metadataValid &&
		a.metadata.RequestKind == responses.Prewarm && string(a.request.Fields["generate"]) == "false"
	if !a.approvalReview && a.executor.Observations != nil {
		automatic := a.executor.Exchange != nil && a.executor.Exchange.State != nil && a.executor.Exchange.State.Automatic()
		var err error
		a.prepared, err = a.executor.Observations.Prepare(a.startCtx, &a.request, Preparation{
			Session: a.sessionID, Thread: a.threadID, Metadata: a.metadata,
			MetadataValid: a.metadataValid, Prewarm: a.prewarm, Automatic: automatic, Trace: a.trace,
		})
		if err != nil {
			return fmt.Errorf("prepare mekugi response proxy: %w", err)
		}
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
	compaction := !a.approvalReview && a.metadataValid && a.metadata.RequestKind == responses.Compaction
	if compaction && a.executor.CompactPrompt != nil {
		err = codexresponses.PrepareCompactTurn(&a.request, a.executor.CompactPrompt())
		if err != nil {
			return err
		}
		metadata, err := responses.ReplaceField([]byte(a.headers.Get(codexresponses.TurnMetadataHeader)), "request_kind", []byte(`"turn"`))
		if err != nil {
			return err
		}
		metadata, err = responses.ReplaceField(metadata, "compaction", []byte(`null`))
		if err != nil {
			return err
		}
		a.headers = a.headers.Clone()
		a.headers.Set(codexresponses.TurnMetadataHeader, string(metadata))
		if raw := a.request.Fields["client_metadata"]; len(raw) != 0 && string(raw) != "null" {
			value, err := json.Marshal(string(metadata))
			if err != nil {
				return err
			}
			a.request.Fields["client_metadata"], err = responses.ReplaceField(raw, codexresponses.TurnMetadataHeader, value)
			if err != nil {
				return err
			}
		}
	}
	attachmentsProjected, attachmentIDs := ProjectFileAttachments(&a.request)
	// Match attachment sources before compact skill rewriting drops native paths.
	if !a.approvalReview && a.executor.Observations != nil {
		if guidance := a.executor.Guidance; guidance != nil {
			if err := ProjectInstructions(&a.request, func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
				return guidance.AfterAttachments(input, a.executor.SkillsManager)
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
	if observer, ok := a.prepared.(interface {
		ObservePreparedContent(*codexresponses.Request, map[int]string)
	}); ok {
		observer.ObservePreparedContent(&a.request, attachmentIDs)
	}
	if exchange := a.executor.Exchange; exchange != nil {
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
	if compaction && a.executor.CompactPrompt != nil {
		a.request.CachedInput, a.request.RebaseInput = 0, true
	}
	nativeWire, err := a.request.IncrementalBody(nativeBody, !inferencegateway.IsThirdPartyModel(a.request.Model()))
	if err != nil {
		return err
	}
	if exchange := a.executor.Exchange; exchange != nil && exchange.State.Automatic() {
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
	if exchange := a.executor.Exchange; exchange != nil && exchange.State.Automatic() {
		debugWire = nil
	}
	a.debug.instructions(projectedBody, debugWire, a.headers, a.sessionID, a.debugID, a.request.CachedInput)
	if !a.prewarm {
		if a.prepared != nil {
			a.usageTracker = a.prepared.Usage()
		} else if a.executor.Observations != nil && a.metadataValid {
			a.usageTracker = a.executor.Usage.Observation(
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
	a.response, err = a.executor.Provider.ForwardExecution(
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
		return fmt.Errorf("execute request: %w", ForwardError(err))
	}
	if a.usageTracker != nil {
		a.usageTracker.Price = PinnedOpenCodePrice(a.response.Price)
	}
	a.hooks.Transport = a.response.Transport
	a.finalization.upstreamStatusCode = a.response.StatusCode
	a.finalization.failurePhase = requestFailureInspectResponse
	if a.prepared != nil {
		a.prepared.BindExecution(a.executionCtx)
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
		if a.prepared != nil {
			a.responseTransform = codexresponses.Compose(a.responseTransform, a.prepared)
		}
	}
	if a.prepared != nil && a.responseTransform == nil {
		a.finalization.failurePhase = requestFailureTransform
		if err := a.prepared.Finish(false); err != nil {
			a.response.Body.Close()
			return fmt.Errorf("record mekugi request overhead: %w", err)
		}
	}
	a.hooks.OnUsage = func(counts inference.Usage) {
		a.finalization.observation.usageCounts = counts
		a.finalization.observation.usageObserved = true
		if a.usageTracker != nil {
			a.usageTracker.ReceivedAt = a.hooks.ReceivedAt
		}
		if a.response.StatusCode >= http.StatusOK && a.response.StatusCode < http.StatusMultipleChoices {
			if a.prepared != nil {
				a.prepared.ObserveUsage(counts)
			} else {
				a.usageTracker.Observe(counts)
			}
		}
		throughput := accounting.MeasureThroughput(counts, a.providerStarted, a.hooks.ReceivedAt)
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
		a.finalization.upstreamTerminalState, err = CopyResponse(
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
		return fmt.Errorf("execute request: %w", ErrMissingTerminal)
	}
	if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
		for name, values := range a.response.Header {
			if http.CanonicalHeaderKey(name) != "Content-Length" {
				writer.Header()[name] = slices.Clone(values)
			}
		}
		writer.WriteHeader(a.response.StatusCode)
	}
	if a.streamResponse {
		if writer, ok := a.executor.Output.(http.ResponseWriter); ok {
			a.finalization.failurePhase = requestFailureWriteResponse
			if err := http.NewResponseController(writer).Flush(); err != nil {
				a.response.Body.Close()
				return fmt.Errorf("execute request: flush response headers: %w", err)
			}
		}
	}
	if stagedBody != nil {
		a.finalization.failurePhase = requestFailureWriteResponse
		if _, err := a.executor.Output.Write(stagedBody); err != nil {
			return fmt.Errorf("execute request: copy upstream response: %w", err)
		}
		codexresponses.ConfirmDelivery(a.responseTransform, stagedBody)
	} else {
		var err error
		a.finalization.failurePhase = requestFailureInspectResponse
		a.finalization.upstreamTerminalState, err = CopyResponse(
			a.executor.Output,
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
		return fmt.Errorf("execute request: %w", ErrMissingTerminal)
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
	if !a.approvalReview && a.finishUsage && a.executor.Naming != nil && a.metadataValid && a.metadata.RequestKind == responses.Turn && !a.prewarm &&
		a.response.StatusCode >= 200 && a.response.StatusCode < 300 &&
		a.finalization.upstreamTerminalState == responses.TerminalCompleted {
		a.executor.Naming.Completed(a.threadID, a.headers)
	}
}

func (a *requestAttempt) finish(requestErr error) error {
	if a.finishUsage {
		a.usageTracker.Finish()
	}
	if a.prepared != nil {
		a.prepared.Close()
	}

	a.finalization.finish(a.executionCtx, requestErr, a.executor.Output, a.executor.Issues)
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
		deliveryErr := codexresponses.WriteEvent(a.executor.Output, payload)
		requestErr = errors.Join(requestErr, deliveryErr)
	}
	fields := map[string]any{
		"event": "request_complete", "request_id": a.debugID,
		"client_request_id": a.headers.Get("x-client-request-id"),
		"thread_id":         codexresponses.ThreadID(a.headers),
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

	if a.hooks.StreamDiagnostics != nil && a.hooks.StreamDiagnostics.CopyStop != "" {
		fields["response_stream"] = a.hooks.StreamDiagnostics.Snapshot()
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
