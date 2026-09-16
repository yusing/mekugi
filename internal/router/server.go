package router

// Source: main.go:20:551 HTTP lifecycle and Responses proxy execution.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/responses"
)

const (
	defaultListenAddress        = "127.0.0.1:0"
	defaultRewriteMode          = "mekugi"
	defaultModelProtocol        = "native"
	defaultRequestTimeout       = 10 * time.Minute
	defaultStreamIdleTimeout    = 4 * time.Minute
	requestBodyReadTimeout      = 30 * time.Second
	shutdownTimeout             = 5 * time.Second
	responsesRequestBufferBytes = 32 << 20
	modelsResponseBufferBytes   = 8 << 20
)

var errUpstreamResponseWithoutTerminal = errors.New("upstream Responses response ended without a terminal state")

// Session is available only after initialization and listener binding succeed.
type Session struct {
	BaseURL        string
	JournalEnabled bool
	GrokEnabled    bool
	AXReadOutput   string
	// EnableLiveDiff arms a best-effort pane on the first selected turn workspace.
	EnableLiveDiff    func()
	FrontendDirectory string
}

// RunSession owns the private router for one wrapped Codex process.
// artifacts receives retained debug paths at completion, even if startup was canceled.
func RunSession(ctx context.Context, args []string, issues *CriticalErrors, ready func(Session), artifacts func([]string)) (runErr error) {
	flags := newRouterFlags(io.Discard)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if *flags.mode != "mekugi" && *flags.mode != "passthrough" {
		return errors.New("--mode must be mekugi or passthrough")
	}
	if *flags.modelProtocol != "native" && *flags.modelProtocol != "ctp2" {
		return errors.New("--model-protocol must be native or ctp2")
	}
	protocolSet := false
	mainMentorSet := false
	mentorSet := false
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "model-protocol":
			protocolSet = true
		case "main-mentor-handoff":
			mainMentorSet = true
		case "mentor-handoff":
			mentorSet = true
		}
	})
	if *flags.mode == "passthrough" {
		if protocolSet && *flags.modelProtocol != "native" {
			return errors.New("--model-protocol ctp2 requires --mode mekugi")
		}
		if mentorSet && *flags.mentorHandoffEnabled {
			return errors.New("--mentor-handoff requires --mode mekugi")
		}
		if mainMentorSet && *flags.mainMentorHandoffEnabled {
			return errors.New("--main-mentor-handoff requires --mode mekugi")
		}
		*flags.modelProtocol = "native"
		*flags.mainMentorHandoffEnabled = false
		*flags.mentorHandoffEnabled = false
	}
	if *flags.grokEnabled && *flags.mode != "mekugi" {
		return errors.New("--grok requires --mode mekugi")
	}
	if !*flags.grokEnabled && *flags.grokAuthFile != "" {
		return errors.New("--grok-auth-file requires --grok")
	}
	if *flags.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if *flags.streamIdleTimeout <= 0 {
		return errors.New("--stream-idle-timeout must be positive")
	}
	if err := validateAXOutputAliases(os.Getenv(capturer.AXReadOutputEnvironment), *flags.captureOutput, *flags.metricsOutput); err != nil {
		return err
	}
	debug, err := openDebugOutput(flags)
	if err != nil {
		return err
	}
	if debug != nil {
		ctx = context.WithValue(ctx, debugContextKey{}, debug)
	}
	defer func() {
		if debug != nil {
			debug.event(map[string]any{"event": "router_stop", "failed": runErr != nil})
			runErr = errors.Join(runErr, debug.close())
			if artifacts != nil {
				artifacts(debug.paths)
			}
		}
	}()
	capture, err := capturer.New(capturer.Config{Output: *flags.captureOutput, Mode: *flags.mode, ModelProtocol: *flags.modelProtocol})
	if err != nil {
		return fmt.Errorf("initialize capture: %w", err)
	}
	var metricsFile *os.File
	if *flags.metricsOutput != "" {
		metricsPath, err := filepath.Abs(*flags.metricsOutput)
		if err != nil {
			return errors.Join(err, capture.Close())
		}
		capturePath, _ := filepath.Abs(*flags.captureOutput)
		if *flags.captureOutput != "" && metricsPath == capturePath {
			return errors.Join(errors.New("capture-output and metrics-output must use different files"), capture.Close())
		}
		if *flags.captureOutput != "" {
			captureInfo, captureErr := os.Stat(capturePath)
			metricsInfo, metricsErr := os.Stat(metricsPath)
			if captureErr == nil && metricsErr == nil && os.SameFile(captureInfo, metricsInfo) {
				return errors.Join(errors.New("capture-output and metrics-output must use different files"), capture.Close())
			}
		}
		metricsFile, err = os.OpenFile(metricsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return errors.Join(fmt.Errorf("open metrics output: %w", err), capture.Close())
		}
	}
	defer func() {
		if metricsFile != nil {
			runErr = errors.Join(runErr, capture.WriteMetrics(metricsFile), metricsFile.Close())
		}
		runErr = errors.Join(runErr, capture.Close())
	}()
	provider := newProviderClient(codexBaseURL, nil)
	provider.httpClient.Transport = capture.Transport(provider.httpClient.Transport)
	provider.streamIdleTimeout = *flags.streamIdleTimeout
	provider.enableWebSockets(ctx)
	// Cover early setup returns before ctx is canceled; close is idempotent
	// with enableWebSockets' normal context-shutdown callback.
	defer provider.websockets.close()
	if *flags.grokEnabled {
		apiKey := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		path := *flags.grokAuthFile
		if path == "" && apiKey == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("locate Grok credentials: %w", err)
			}
			path = filepath.Join(home, ".grok", "auth.json")
		}
		auth := newGrokAuth(path, apiKey)
		client := withDialTimeout(nil)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client.Transport = capture.Transport(client.Transport)
		provider.grok = &grokClient{httpClient: client, auth: auth, streamIdleTimeout: *flags.streamIdleTimeout}
	}
	var frontendDirectory string
	var dataDirectory string
	var mekugiCalls *mekugiProxy
	var compactTokens *ctp2Codec
	var mentor *mentorHandoff
	if *flags.mode == "mekugi" {
		var err error
		dataDirectory, err = mekugiDataDirectory()
		if err != nil {
			return fmt.Errorf("initialize mekugi response proxy: %w", err)
		}
		if *flags.modelProtocol == "ctp2" {
			compactTokens, err = newCTP2Codec()
			if err != nil {
				return fmt.Errorf("initialize compact token protocol: %w", err)
			}
		}
	}
	if *flags.mentorHandoffEnabled || *flags.mainMentorHandoffEnabled {
		mentor = newMentorHandoff(*flags.mainMentorHandoffEnabled, *flags.mentorHandoffEnabled)
	}
	titles := newSessionTitleCache()
	if *flags.mode == "mekugi" {
		translator := newInProcessMekugiTranslator(dataDirectory)
		customizedInstructions, err := codexModelInstructionFileConfigured()
		if err != nil {
			return fmt.Errorf("initialize model instruction rewriting: %w", err)
		}
		registry, err := buildToolRegistry(ctx, dataDirectory, translator.ToolDescription(), os.Getenv("MEKUGI_DIAGNOSE") == "1")
		if err != nil {
			return fmt.Errorf("initialize tool registry: %w", err)
		}
		if err := registry.installFrontends(); err != nil {
			return errors.Join(
				fmt.Errorf("initialize configured tool frontends: %w", err),
				registry.Close(),
			)
		}
		defer func() {
			runErr = errors.Join(runErr, registry.Close())
		}()
		frontendDirectory = registry.frontendDirectory
		replayDirectory, err := defaultMekugiReplayDirectory()
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		replayStore, err := openMekugiReplayStore(replayDirectory)
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		mekugiCalls = newMekugiProxy(translator, registry, customizedInstructions, compactTokens != nil, titles)
		mekugiCalls.noticeSink = issues.addNotice
		replayStore.storageNotice = func(session, message string) { issues.addNotice(session, "storage_cleanup", message) }
		mekugiCalls.commentary.debug = debug
		var stopLiveDiff func()
		mekugiCalls.autoLiveDiff, stopLiveDiff = newAutoLiveDiff(ctx, replayDirectory)
		mekugiCalls.autoLiveDiff.notice = func(category, message string) { issues.addNotice("", category, message) }
		replayStore.liveDiff = mekugiCalls.autoLiveDiff.events.publish
		defer stopLiveDiff()
		mekugiCalls.replayStore = replayStore
		defer func() {
			runErr = errors.Join(runErr, mekugiCalls.Close())
		}()
	}

	listener, err := net.Listen("tcp", defaultListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	address := listener.Addr().String()
	if mekugiCalls != nil {
		mekugiCalls.autoLiveDiff.events.setEndpoint("http://" + address + liveDiffEventsPath)
		mekugiCalls.commentaryEndpoint, err = commentaryPublisherURL(address)
		if err != nil {
			return fmt.Errorf("initialize commentary publisher: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveDashboard)
	mux.HandleFunc("GET /api/metrics", capture.ServeHTTP)
	mux.HandleFunc("GET /v1/models", modelsHandler(provider, issues))
	if mekugiCalls != nil {
		mux.HandleFunc("GET "+liveDiffEventsPath, mekugiCalls.autoLiveDiff.events.serveEvents)
		mux.HandleFunc("POST "+liveDiffEventsPath+"/producer", mekugiCalls.autoLiveDiff.events.serveProducer)
		mux.HandleFunc("POST "+commentaryPublisherPath, mekugiCalls.commentary.serveHTTP)
	}
	webSocketEndpoint := responsesWebSocketHandler(ctx, *flags.timeout, provider, issues, mekugiCalls, compactTokens, mentor)
	defer webSocketEndpoint.Close()
	mux.Handle("GET /v1/responses", webSocketEndpoint)
	mux.HandleFunc("POST /v1/responses", responsesHandler(ctx, *flags.timeout, provider, issues, mekugiCalls, compactTokens, mentor))

	server := &http.Server{
		ErrorLog:          log.New(io.Discard, "", 0), // Disable net/http terminal diagnostics while Codex owns it.
		Addr:              defaultListenAddress,
		Handler:           capture.Handler(debug.handler(mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       requestBodyReadTimeout,
		IdleTimeout:       2 * time.Minute,
	}
	baseURL := "http://" + address + "/v1"
	serverError := make(chan error, 1)
	go func() {
		serverError <- server.Serve(listener)
	}()
	if ready != nil && ctx.Err() == nil {
		session := Session{BaseURL: baseURL, FrontendDirectory: frontendDirectory, GrokEnabled: *flags.grokEnabled, JournalEnabled: *flags.mode == "mekugi"}
		if mekugiCalls != nil {
			session.EnableLiveDiff = mekugiCalls.autoLiveDiff.enable
		}
		if debug != nil {
			session.AXReadOutput = debug.paths[4]
		}
		ready(session)
	}
	select {
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, server.Close())
		}
		serveErr := <-serverError
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func modelsHandler(provider *providerClient, issues *CriticalErrors) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		tracked := &trackedResponseWriter{ResponseWriter: writer}
		writer = tracked
		defer func() {
			if tracked.statusCode >= 400 && request.Context().Err() == nil {
				issues.record(&requestFinalization{sessionID: request.Header.Get(sessionIDHeader), failurePhase: requestFailureForward,
					upstreamStatusCode: tracked.statusCode, observation: requestObservation{outcome: requestOutcomeFailed}}, nil)
			}
		}()

		response, err := provider.forwardModels(request.Context(), request.Header, request.URL.RawQuery)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, modelsResponseBufferBytes+1))
		if err != nil {
			http.Error(writer, fmt.Sprintf("read upstream models response: %v", err), http.StatusBadGateway)
			return
		}
		if len(body) > modelsResponseBufferBytes {
			http.Error(writer, "upstream models response exceeds the router buffer budget", http.StatusBadGateway)
			return
		}
		for _, name := range []string{"Content-Type", "Cache-Control", "ETag"} {
			for _, value := range response.Header.Values(name) {
				writer.Header().Add(name, value)
			}
		}
		writer.WriteHeader(response.StatusCode)
		_, _ = writer.Write(body)
	}
}

func responsesHandler(
	lifecycle context.Context,
	responseStartTimeout time.Duration,
	provider responseProvider,
	issues *CriticalErrors,
	mekugiCalls *mekugiProxy,
	compactTokens *ctp2Codec,
	mentor *mentorHandoff,
) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		trackedWriter := &trackedResponseWriter{ResponseWriter: writer}
		body, err := readResponsesRequest(io.LimitReader(request.Body, responsesRequestBufferBytes+1))
		if err != nil {
			http.Error(trackedWriter, err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > responsesRequestBufferBytes {
			http.Error(trackedWriter, "Responses request exceeds the router buffer budget", http.StatusRequestEntityTooLarge)
			return
		}
		parsedRequest, err := parseResponsesRequest(body)
		if err != nil {
			http.Error(trackedWriter, err.Error(), http.StatusBadRequest)
			return
		}
		startCtx, executionCtx, cancelRequest := requestContexts(request.Context(), lifecycle, responseStartTimeout)
		defer cancelRequest()
		sessionID := routingSessionID(request.Header, parsedRequest)
		if err := executeRequest(startCtx, executionCtx, parsedRequest, request.Header, sessionID, provider, trackedWriter, issues, mekugiCalls, compactTokens, mentor); err != nil {
			writeRequestError(trackedWriter, err)
		}
	}
}

func requestContexts(requestCtx, serverCtx context.Context, responseStartTimeout time.Duration) (context.Context, context.Context, func()) {
	executionCtx, cancelExecution := context.WithCancelCause(requestCtx)
	stopServerCancellation := context.AfterFunc(serverCtx, func() { cancelExecution(errRouterShutdown) })
	startCtx, cancelStart := context.WithTimeoutCause(executionCtx, responseStartTimeout, errResponseStartTimeout)
	return startCtx, executionCtx, func() {
		stopServerCancellation()
		cancelStart()
		cancelExecution(nil)
	}
}

type trackedResponseWriter struct {
	http.ResponseWriter

	committed  bool
	statusCode int
}

func (w *trackedResponseWriter) WriteHeader(statusCode int) {
	if w.committed {
		return
	}
	w.committed = true
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *trackedResponseWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *trackedResponseWriter) FlushError() error {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *trackedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func writeRequestError(writer *trackedResponseWriter, requestErr error) {
	if !writer.committed {
		status := http.StatusBadGateway
		if _, ok := errors.AsType[*requestCompatibilityError](requestErr); ok {
			status = http.StatusBadRequest
		}
		http.Error(writer, requestErr.Error(), status)
	}
}

type requestFailurePhase string

const (
	requestFailurePrepare            requestFailurePhase = "prepare"
	requestFailureForward            requestFailurePhase = "forward"
	requestFailureInspectResponse    requestFailurePhase = "inspect_response"
	requestFailureStreamIdleTimeout  requestFailurePhase = "stream_idle_timeout"
	requestFailureTransform          requestFailurePhase = "transform"
	requestFailureWriteResponse      requestFailurePhase = "write_response"
	requestFailureTerminalValidation requestFailurePhase = "terminal_validation"
)

type requestFinalization struct {
	observeCriticalNotice func(source, text string)
	observation           requestObservation
	sessionID             string
	failurePhase          requestFailurePhase
	upstreamStatusCode    int
	diagnosticReference   string
	diagnosticCode        string
	upstreamTerminalState responseTerminalState
}

func (f *requestFinalization) finish(
	ctx context.Context,
	requestErr error,
	output io.Writer,
	issues *CriticalErrors,
) error {
	responseStarted := requestResponseStarted(output)
	if f.observation.outcome == requestOutcomeUnknown {
		switch {
		case errors.Is(requestErr, context.DeadlineExceeded):
			f.observation.outcome = requestOutcomeTimedOut
		case errors.Is(requestErr, context.Canceled) && errors.Is(ctx.Err(), context.Canceled):
			if errors.Is(requestErr, errUpstreamStreamIdleTimeout) {
				f.failurePhase = requestFailureInspectResponse
			}
			if responseStarted {
				f.observation.outcome = requestOutcomeCanceledAfterResponse
			} else {
				f.observation.outcome = requestOutcomeCanceledBeforeResponse
			}
		case errors.Is(requestErr, errUpstreamStreamIdleTimeout):
			f.observation.outcome = requestOutcomeStreamIdleTimedOut
		default:
			f.observation.outcome = requestOutcomeFailed
		}
	}
	issues.record(f, requestErr)

	return nil
}

func requestResponseStarted(output io.Writer) bool {
	writer, ok := output.(*trackedResponseWriter)
	return ok && writer.committed
}

func (f *requestFinalization) classifyCopyError(err error) {
	switch {
	case errors.Is(err, errUpstreamStreamIdleTimeout):
		f.failurePhase = requestFailureStreamIdleTimeout
	case errors.Is(err, errResponseTransform):
		f.failurePhase = requestFailureTransform
	case errors.Is(err, errResponseWrite):
		f.failurePhase = requestFailureWriteResponse
	default:
		f.failurePhase = requestFailureInspectResponse
	}
}

type journalFinishResponseProvider struct {
	stream bool
}

func (provider journalFinishResponseProvider) forwardExecution(context.Context, context.Context, []byte, http.Header, string) (*http.Response, error) {
	id := "resp_mekugi_journal_" + rand.Text()
	response := map[string]any{
		"id": id, "object": "response", "status": "completed", "output": []any{},
	}
	if !provider.stream {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(mustMarshalJSON(response))),
		}, nil
	}
	created := mustMarshalJSON(map[string]any{
		"type":     responses.Created,
		"response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}},
	})
	completed := mustMarshalJSON(map[string]any{
		"type": responses.Completed, "response": response,
	})
	body := append([]byte("data: "), created...)
	body = append(body, '\n', '\n')
	body = append(body, []byte("data: ")...)
	body = append(body, completed...)
	body = append(body, '\n', '\n')
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

// executeRequest executes a parsed Responses request through the router pipeline.
func executeRequest(
	ctx context.Context,
	executionCtx context.Context,
	parsedRequest parsedResponsesRequest,
	headers http.Header,
	sessionID string,
	provider responseProvider,
	output io.Writer,
	issues *CriticalErrors,
	mekugiCalls *mekugiProxy,
	compactTokens *ctp2Codec,
	mentor *mentorHandoff,
) (requestErr error) {
	journalOriginal := maps.Clone(parsedRequest.fields)
	journalStartWindow := journalRequestWindow(ctx)
	hooks := &responseHooks{}
	finalization := requestFinalization{failurePhase: requestFailurePrepare}
	debug, debugID := debugRequest(ctx)
	if debug != nil {
		hooks.streamDiagnostics = &streamDiagnostics{}
	}
	if exchange, ok := provider.(*webSocketExchange); ok {
		exchange.streamDiagnostics = hooks.streamDiagnostics
	}
	started := time.Now()
	trace := featureUsageTrace{debug: debug, requestID: debugID, threadID: codexThreadID(headers), sessionID: sessionID}
	if debug != nil {
		trace.summary = &featureUsageSummary{counts: make(map[string]uint64)}
	}
	commentaryObserved := false
	if sessionID != "" {
		finalization.sessionID = sessionID
	}

	defer func() {
		requestErr = errors.Join(requestErr, finalization.finish(executionCtx, requestErr, output, issues))
		hooks.finish(finalization.completion())
		fields := map[string]any{
			"event": "request_complete", "request_id": debugID,
			"client_request_id": headers.Get("x-client-request-id"), "thread_id": codexThreadID(headers),
			"session_id": sessionID, "outcome": finalization.observation.outcome.String(),
			"phase": finalization.failurePhase, "upstream_status": finalization.upstreamStatusCode,
			"duration_ms": time.Since(started).Milliseconds(),
		}
		if captureID, sequence := capturer.RequestCorrelation(ctx); captureID != "" {
			fields["capture_id"], fields["request_sequence"] = captureID, sequence
		}
		if cause := requestCancellationCause(executionCtx, requestErr); cause != "" {
			fields["cancellation_cause"] = cause
		}
		if errors.Is(requestErr, errUpstreamStreamIdleTimeout) {
			fields["idle_timeout_observed"] = true
		}
		trace.finish(commentaryObserved, requestErr == nil && finalization.upstreamStatusCode >= 200 && finalization.upstreamStatusCode < 300)
		if hooks.streamDiagnostics != nil && hooks.streamDiagnostics.CopyStop != "" {
			fields["response_stream"] = hooks.streamDiagnostics.snapshot()
		}
		if finalization.diagnosticReference != "" {
			fields["diagnostic_reference"] = finalization.diagnosticReference
			fields["diagnostic_code"] = finalization.diagnosticCode
		}
		debug.event(fields)

	}()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("prepare request: %w", withRequestStartCause(ctx, err))
	}
	metadata, metadataValid := decodeCodexTurnMetadata(headers)
	if metadataValid {
		capturer.ObserveRequestKind(ctx, string(metadata.RequestKind))
	}
	threadID := codexThreadID(headers)
	if mekugiCalls != nil && metadataValid && !metadata.activityIdentityInvalid && (metadata.ThreadID == "" || metadata.ThreadID == threadID) {
		finalization.observeCriticalNotice = func(source, text string) { mekugiCalls.activity.collect(threadID, source, "error", text) }
	}
	parsedRequest.filterInput(func(map[string]json.RawMessage) { issues.stripInput(&parsedRequest, sessionID) })
	notices := issues.transform(sessionID, metadata.SubagentKind != "")
	defer func() { notices.finish(requestErr == nil) }()
	handoffRequest, err := mentor.prepare(headers, metadata, metadataValid, &parsedRequest)
	if err != nil {
		return fmt.Errorf("prepare Mentor Handoff: %w", err)
	}
	if exchange, ok := provider.(*webSocketExchange); ok && exchange.history != nil {
		if exchange.automatic {
			parent := exchange.history.parent
			if parent == nil || parent.providerModel == "" {
				return errors.New("automatic WebSocket successor has no retained provider model")
			}
			parsedRequest.fields["model"] = mustMarshalJSON(parent.providerModel)
			if len(parent.providerReasoning) == 0 {
				delete(parsedRequest.fields, "reasoning")
			} else {
				parsedRequest.fields["reasoning"] = parent.providerReasoning
			}
		}
		exchange.history.providerModel = parsedRequest.model()
		exchange.history.providerReasoning = parsedRequest.fields["reasoning"]
	}
	var mekugiTransform *mekugiResponseTransform
	if handoffRequest != nil {
		hooks.output = &handoffRequest.observation
	}
	hooks.onFinished = func(result requestCompletion) {
		if handoffRequest == nil {
			return
		}
		progress := handoffRequest.record(result)
		if progress.transitioned && mekugiTransform != nil {
			broker := mekugiCalls.commentary
			token := broker.subscribeThread(mekugiTransform.historySessionID, threadID, mekugiTransform.commentaryAuthor)
			broker.publish(token, "Mentor handoff complete.", false)
		}
	}
	// Only the WebSocket provider guarantees non-generating warmup for every
	// supported model. HTTP requests must retain ordinary preparation checks.
	_, webSocketRequest := provider.(*webSocketExchange)
	prewarm := webSocketRequest && metadataValid && metadata.RequestKind == responses.Prewarm && string(parsedRequest.fields["generate"]) == "false"
	if prewarm {
		// Prewarm retains native instructions, including no CTP decoding guide.
		compactTokens = nil
	}
	if mekugiCalls != nil && !prewarm {
		mekugiTransform, err = mekugiCalls.prepareRequest(
			ctx,
			&parsedRequest,
			sessionID,
			codexThreadID(headers),
			metadata,
			metadataValid,
		)
		if err != nil {
			return fmt.Errorf("prepare mekugi response proxy: %w", err)
		}
		if mekugiTransform == nil {
			// Compaction and auxiliary structured turns do not receive the
			// Mekugi instructions needed to decode CTP text.
			compactTokens = nil
		}
	}
	if mekugiCalls != nil {
		workspace, usable := usableRoutingDirectory(metadata.Directories)
		if mekugiTransform != nil {
			workspace, usable = mekugiTransform.directory, true
		}
		if usable {
			notices.retain(ctx, mekugiCalls.replayStore, workspace)
		} else {
			// Without a canonical namespace the notice cannot be stripped from a
			// later ordinary turn, so keep it queued instead of emitting it.
			notices.suppress()
		}
	}
	if mekugiTransform != nil && metadataValid {
		mekugiCalls.autoLiveDiff.observe(mekugiTransform.directory, mekugiTransform.threadID, metadata)
	}
	if mekugiTransform != nil {
		mekugiTransform.featureTrace = trace
		commentaryObserved = true
		defer mekugiTransform.Close()
	}
	syntheticJournalFinish := mekugiTransform != nil && mekugiTransform.shellFinishRequested
	if syntheticJournalFinish {
		// A shell finish is already the terminal operation for this request.
		// Keep the normal response transformation and journal delivery, but do
		// not ask the provider to generate a follow-up response.
		handoffRequest = nil
		hooks.output = nil
		mekugiTransform.journalTerminal = true
		compactTokens = nil
	}

	var bridge *subagentBridge
	var compactTransform *ctp2ResponseTransform
	bridgeProvider := provider
	if exchange, ok := provider.(*webSocketExchange); ok {
		bridgeProvider = exchange.session.provider
	}
	client, _ := bridgeProvider.(*providerClient)
	grokEnabled := client != nil && client.grok != nil
	if !syntheticJournalFinish && (mekugiTransform != nil || grokEnabled) {
		bridge, err = prepareSubagentBridge(&parsedRequest, grokEnabled)
		if err != nil {
			return fmt.Errorf("prepare collaboration bridge: %w", err)
		}
	}
	nativeBody, err := parsedRequest.wireBody(parsedRequest.fields)
	if err != nil {
		return fmt.Errorf("encode native Responses request: %w", err)
	}
	var forwardBody []byte
	compactTransform, forwardBody, err = compactTokens.prepareRequest(&parsedRequest, nativeBody)
	if err != nil {
		return fmt.Errorf("prepare compact token protocol: %w", err)
	}
	if forwardBody == nil {
		forwardBody = nativeBody
	}
	if !syntheticJournalFinish {
		if exchange, ok := provider.(*webSocketExchange); ok {
			started := time.Now()
			if err := exchange.reconcileProviderHistory(&parsedRequest, forwardBody); err != nil {
				return err
			}
			debug.event(map[string]any{
				"event": "provider_history_reconciliation", "request_id": debugID,
				"reason": exchange.reconciliationReason, "reused_input_items": parsedRequest.cachedInput,
				"duration_us": time.Since(started).Microseconds(),
			})
		}
	}
	nativeWire, err := parsedRequest.incrementalBody(nativeBody)
	if err != nil {
		return err
	}
	if !syntheticJournalFinish {
		if exchange, ok := provider.(*webSocketExchange); ok && exchange.automatic {
			capturer.ObserveNativeRequest(ctx, nil)
		} else {
			capturer.ObserveNativeRequest(ctx, nativeWire)
		}
	}
	projectedBody := forwardBody
	finalization.failurePhase = requestFailureForward
	cacheKey := parsedRequest.promptCacheKey()
	if cacheKey == "" {
		cacheKey = sessionID
	}
	forwardBody, err = parsedRequest.incrementalBody(forwardBody)
	if err != nil {
		return err
	}
	debugWire := forwardBody
	if exchange, ok := provider.(*webSocketExchange); ok && exchange.automatic {
		debugWire = nil
	}
	debug.instructions(projectedBody, debugWire, headers, sessionID, debugID, parsedRequest.cachedInput)
	var usageTracker *threadUsageObservation
	if !prewarm && !syntheticJournalFinish {
		if mekugiTransform != nil {
			usageTracker = mekugiTransform.usageTracker
		} else if mekugiCalls != nil && metadataValid {
			usageTracker = mekugiCalls.usage.observation(threadID, metadata.ThreadID, parsedRequest.model(), usageServiceTier(parsedRequest.fields["service_tier"]))
		}
	}
	forwardProvider := provider
	if syntheticJournalFinish {
		forwardProvider = journalFinishResponseProvider{stream: parsedRequest.streamResponse}
	}
	response, err := forwardProvider.forwardExecution(ctx, executionCtx, forwardBody, headers, cacheKey)
	// Definite HTTP rejections did not admit inference. Transport failures and
	// accepted requests may have consumed tokens even without a usable terminal.
	rejection, rejectedUpgrade := errors.AsType[*webSocketStatusError](err)
	if rejectedUpgrade {
		finalization.upstreamStatusCode = rejection.status
	}
	if !rejectedUpgrade && (response == nil || response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices) {
		defer usageTracker.finish()
	}
	if err != nil {
		err = withRequestStartCause(ctx, err)
		return fmt.Errorf("execute request: %w", forwardCriticalDiagnostic(err))
	}
	finalization.upstreamStatusCode = response.StatusCode
	finalization.failurePhase = requestFailureInspectResponse
	if mekugiTransform != nil {
		mekugiTransform.ctx = executionCtx
	}
	streamResponse, err := prepareUpstreamBody(response, parsedRequest.streamResponse)
	if err != nil {
		response.Body.Close()
		return fmt.Errorf("execute request: inspect upstream response: %w", err)
	}
	defer func() {
		if mekugiTransform != nil {
			mekugiTransform.ReleaseDelivery()
		}
	}()
	var responseTransform responseTransformer
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		if compactTransform != nil {
			responseTransform = composeResponseTransformers(responseTransform, compactTransform)
		}
		if bridge != nil {
			responseTransform = composeResponseTransformers(responseTransform, bridge)
		}
		if mekugiTransform != nil {
			responseTransform = composeResponseTransformers(responseTransform, mekugiTransform)
		}
		if notices != nil {
			responseTransform = composeResponseTransformers(responseTransform, notices)
		}
	}
	if mekugiTransform != nil && responseTransform == nil {
		finalization.failurePhase = requestFailureTransform
		if err := mekugiTransform.Finish(false); err != nil {
			response.Body.Close()
			return fmt.Errorf("record mekugi request overhead: %w", err)
		}
	}
	hooks.onUsage = func(counts tokenCounts) {
		finalization.observation.usageCounts = counts
		finalization.observation.usageObserved = true
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			if mekugiTransform != nil {
				mekugiTransform.observeResponseUsage(counts)
			} else {
				// Compaction has no mekugi response transform, but still consumes
				// provider tokens belonging to the same stable thread.
				usageTracker.observe(counts)
			}
		}

		capturer.ObserveProviderUsage(executionCtx, capturer.ProviderUsage{
			EvidenceComplete: new(!counts.Incomplete && !counts.Inconsistent),
			InputTokens:      counts.InputTokens,
			CachedTokens:     counts.InputTokens - counts.UncachedInputTokens,
			OutputTokens:     counts.OutputTokens,
			ReasoningTokens:  counts.ReasoningTokens,
		})
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		hooks.output = nil
	}
	var stagedBody []byte
	if !streamResponse {
		var staged bytes.Buffer
		finalization.upstreamTerminalState, err = copyUpstreamBodyTransformed(&staged, response, false, responseTransform, hooks)
		if err != nil {
			finalization.classifyCopyError(err)
			return fmt.Errorf("execute request: %w", err)
		}
		stagedBody = staged.Bytes()
	}
	continueJournal := func() error {
		next, err := nextJournalRequest(journalOriginal, mekugiTransform)
		if err != nil {
			return err
		}
		nextCtx, err := continueJournalContext(executionCtx, mekugiTransform)
		if err != nil {
			return err
		}
		mekugiTransform.Close()
		resetJournalExchange(provider)
		start, cancel := context.WithTimeout(nextCtx, journalStartWindow)
		defer cancel()
		finalization.observation.outcome = requestOutcomeCompleted
		finalization.failurePhase = ""
		// Account for this completed response before preparing its successor.
		hooks.finish(finalization.completion())
		// Record missing usage before the continuation can publish cumulative totals.
		usageTracker.finish()
		return executeRequest(start, nextCtx, next, headers, sessionID, provider, output, issues, mekugiCalls, compactTokens, mentor)
	}
	if !streamResponse && mekugiTransform != nil && mekugiTransform.journalContinue {
		return continueJournal()
	}
	if !streamResponse && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && !acceptsResponseEnd(finalization.upstreamTerminalState) {
		finalization.failurePhase = requestFailureTerminalValidation
		return fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
	}
	if writer, ok := output.(http.ResponseWriter); ok {
		for name, values := range response.Header {
			if http.CanonicalHeaderKey(name) != "Content-Length" {
				writer.Header()[name] = append([]string(nil), values...)
			}
		}
		writer.WriteHeader(response.StatusCode)
	}
	if streamResponse {
		if writer, ok := output.(http.ResponseWriter); ok {
			finalization.failurePhase = requestFailureWriteResponse
			if err := http.NewResponseController(writer).Flush(); err != nil {
				response.Body.Close()
				return fmt.Errorf("execute request: flush response headers: %w", err)
			}
		}
	}
	if stagedBody != nil {
		finalization.failurePhase = requestFailureWriteResponse
		if _, err := output.Write(stagedBody); err != nil {
			return fmt.Errorf("execute request: copy upstream response: %w", err)
		}
		confirmResponseDelivery(responseTransform, stagedBody)
		releaseResponseDelivery(responseTransform)
	} else {
		finalization.failurePhase = requestFailureInspectResponse
		finalization.upstreamTerminalState, err = copyUpstreamBodyTransformed(output, response, streamResponse, responseTransform, hooks)
		if err != nil {
			finalization.classifyCopyError(err)
			return fmt.Errorf("execute request: %w", err)
		}
	}
	if streamResponse && mekugiTransform != nil && mekugiTransform.journalContinue {
		return continueJournal()
	}
	if streamResponse && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && !acceptsResponseEnd(finalization.upstreamTerminalState) {
		finalization.failurePhase = requestFailureTerminalValidation
		return fmt.Errorf("execute request: %w", errUpstreamResponseWithoutTerminal)
	}
	finalization.failurePhase = ""
	switch {
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		finalization.observation.outcome = requestOutcomeFailed
		finalization.failurePhase = requestFailureTerminalValidation
	case finalization.upstreamTerminalState == responseTerminalCompleted || finalization.upstreamTerminalState == responseTerminalSteered:
		finalization.observation.outcome = requestOutcomeCompleted
		hooks.finish(finalization.completion())
	case finalization.upstreamTerminalState == responseTerminalFailed:
		finalization.observation.outcome = requestOutcomeFailed
		finalization.failurePhase = requestFailureTerminalValidation
	default:
		finalization.observation.outcome = requestOutcomeFailed
		finalization.failurePhase = requestFailureTerminalValidation
	}
	return nil
}

func acceptsResponseEnd(state responseTerminalState) bool {
	return state.Terminal()
}
