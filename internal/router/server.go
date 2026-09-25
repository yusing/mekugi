package router

// Source: main.go:20:551 HTTP lifecycle and Responses proxy execution.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/capturer"
)

const (
	defaultListenAddress        = "127.0.0.1:0"
	defaultRewriteMode          = "mekugi"
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
	BaseURL                string
	JournalEnabled         bool
	PostCompactRecovery    bool
	GrokEnabled            bool
	OpenCode               OpenCodeConfig
	AXReadOutput           string
	SkillsManagerAvailable bool
	// StartUI starts Codex in the integrated terminal and returns its joined lifetime.
	StartUI              func(context.Context, *exec.Cmd, *os.File, *os.File) (func() error, error)
	FrontendDirectory    string
	NativeTraceDirectory string
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
	mainMentorSet := false
	mentorSet := false
	postCompactSet := false
	exploreFilterSet := false
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "post-compact-recovery":
			postCompactSet = true
		case "explore-filter":
			exploreFilterSet = true
		case "main-mentor-handoff":
			mainMentorSet = true
		case "mentor-handoff":
			mentorSet = true
		}
	})
	if *flags.mode == "passthrough" {
		if mentorSet && *flags.mentorHandoffEnabled {
			return errors.New("--mentor-handoff requires --mode mekugi")
		}
		if mainMentorSet && *flags.mainMentorHandoffEnabled {
			return errors.New("--main-mentor-handoff requires --mode mekugi")
		}
		if postCompactSet && *flags.postCompactRecovery {
			return errors.New("--post-compact-recovery requires --mode mekugi")
		}
		*flags.mainMentorHandoffEnabled = false
		*flags.mentorHandoffEnabled = false
		*flags.postCompactRecovery = false
	}
	if *flags.grokEnabled && *flags.mode != "mekugi" {
		return errors.New("--grok requires --mode mekugi")
	}
	if !*flags.grokEnabled && *flags.grokAuthFile != "" {
		return errors.New("--grok-auth-file requires --grok")
	}
	config, err := loadMekugiConfig()
	if err != nil {
		return err
	}
	// The filter defaults on but needs a credential; only an explicit request
	// for it makes a missing key or passthrough mode a startup error.
	typesafeKey := config.typeSafeAPIKey()
	if exploreFilterSet && *flags.exploreFilter && *flags.mode != "mekugi" {
		return errors.New("--explore-filter requires --mode mekugi")
	}
	if exploreFilterSet && *flags.exploreFilter && typesafeKey == "" {
		return errors.New("--explore-filter requires a TypeSafe API key")
	}
	*flags.exploreFilter = *flags.exploreFilter && typesafeKey != "" && *flags.mode == "mekugi"
	openCode := config.Providers
	if openCode.Enabled() && *flags.mode != "mekugi" {
		return errors.New("OpenCode providers require --mode mekugi")
	}
	if *flags.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if *flags.streamIdleTimeout <= 0 {
		return errors.New("--stream-idle-timeout must be positive")
	}
	if err := validateAXOutputAliases(os.Getenv(capturer.AXReadOutputEnvironment), *flags.captureOutput); err != nil {
		return err
	}
	debug, err := openDebugOutput(flags)
	if err != nil {
		return err
	}
	if debug != nil {
		ctx = context.WithValue(ctx, debugContextKey{}, debug)
	}
	var mekugiCalls *mekugiProxy
	defer func() {
		if debug != nil {
			debug.event(map[string]any{"event": "router_stop", "failed": runErr != nil})
			runErr = errors.Join(runErr, debug.close())
		}
		if artifacts != nil {
			var paths []string
			if debug != nil {
				paths = append(paths, debug.paths...)
			}
			if mekugiCalls != nil {
				paths = append(paths, mekugiCalls.tokenMetricPaths()...)
			}
			if len(paths) != 0 {
				artifacts(paths)
			}
		}
	}()
	capture, err := capturer.New(capturer.Config{Output: *flags.captureOutput, Mode: *flags.mode})
	if err != nil {
		return fmt.Errorf("initialize capture: %w", err)
	}
	var metricsFile *os.File
	if debug != nil {
		metricsFile, err = os.OpenFile(debug.metricsPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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
	if openCode.Enabled() {
		openCode.catalog = newOpenCodeCatalog()
		if err := openCode.catalog.refresh(ctx, false); err != nil {
			log.Printf("OpenCode catalog: refresh/cache update unavailable; retaining last usable metadata")
		}
	}
	provider.serviceTiers = config.ServiceTiers
	provider.opencode = make(map[string]*grokClient)
	for _, service := range openCode.services() {
		client := withDialTimeout(nil)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client.Transport = capture.Transport(client.Transport)
		provider.opencode[service.prefix] = &grokClient{httpClient: client, openCode: &service, streamIdleTimeout: *flags.streamIdleTimeout}
	}
	var frontendDirectory string
	var dataDirectory string
	var mentor *mentorHandoff
	if *flags.mode == "mekugi" {
		var err error
		dataDirectory, err = mekugiDataDirectory()
		if err != nil {
			return fmt.Errorf("initialize mekugi response proxy: %w", err)
		}
	}
	if *flags.mentorHandoffEnabled || *flags.mainMentorHandoffEnabled {
		mentor = newMentorHandoff(*flags.mainMentorHandoffEnabled, *flags.mentorHandoffEnabled)
	}
	titles := newSessionTitleCache()
	if issues != nil {
		issues.persistFailures = true
	}
	if *flags.mode == "mekugi" {
		registry, err := buildToolRegistry(ctx, dataDirectory, os.Getenv("MEKUGI_DIAGNOSE") == "1")
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
		mekugiCalls = newMekugiProxy(registry, titles)
		traceDirectory, traceErr := os.MkdirTemp("", "mekugi-native-trace-")
		if traceErr == nil {
			mekugiCalls.nativeTrace = &nativeToolTrace{directory: traceDirectory}
			defer func() { runErr = errors.Join(runErr, os.RemoveAll(traceDirectory)) }()
		} else {
			issues.addNotice("", "native_trace", "Nested tool confirmation unavailable: "+traceErr.Error())
		}
		if *flags.exploreFilter {
			mekugiCalls.exploreFilter = newExploreFilter(newTypesafeClient(typesafeKey))
		}
		mekugiCalls.noticeSink = issues.addNotice
		replayStore.storageNotice = func(session, message string) { issues.addNotice(session, "storage_cleanup", message) }
		mekugiCalls.commentary.debug = debug
		var stopLiveDiff func()
		mekugiCalls.autoLiveDiff, stopLiveDiff = newAutoLiveDiff(ctx, replayDirectory)
		mekugiCalls.autoLiveDiff.notice = func(category, message string) { issues.addNotice("", category, message) }
		replayStore.liveDiff = mekugiCalls.autoLiveDiff.events.publish
		mekugiCalls.activity.attachPane(newActivityPane(ctx, mekugiCalls.autoLiveDiff.requestActivity))
		defer stopLiveDiff()
		mekugiCalls.replayStore = replayStore
		if issues != nil {
			issues.failureStore = replayStore
		}
		defer func() {
			runErr = errors.Join(runErr, mekugiCalls.Close())
		}()
	}
	skillsManagerAvailable := mekugiCalls != nil && skillsManagerInPath(frontendDirectory)
	if mekugiCalls != nil {
		mekugiCalls.skillsManager = skillsManagerAvailable
	}

	listener, err := net.Listen("tcp", defaultListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	address := listener.Addr().String()
	if mekugiCalls != nil {
		mekugiCalls.commentaryEndpoint, err = commentaryPublisherURL(address)
		if err != nil {
			return fmt.Errorf("initialize commentary publisher: %w", err)
		}
		retentionCtx, stopRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			defer close(retentionDone)
			mekugiCalls.replayStore.runRetentionSweeps(retentionCtx, func() {
				issues.addNotice("", "storage_cleanup_failure", "Mekugi could not complete background storage cleanup. Session requests continue unless storage reaches its limit.")
			})
		}()
		defer func() {
			stopRetention()
			<-retentionDone
		}()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveDashboard)
	mux.HandleFunc("GET /api/metrics", capture.ServeHTTP)
	mux.HandleFunc("GET /v1/models", modelsHandler(provider, issues))
	if mekugiCalls != nil {
		mux.HandleFunc("POST "+commentaryPublisherPath, mekugiCalls.commentary.serveHTTP)
	}
	webSocketEndpoint := responsesWebSocketHandler(ctx, *flags.timeout, provider, issues, mekugiCalls, mentor)
	defer webSocketEndpoint.Close()
	mux.Handle("GET /v1/responses", webSocketEndpoint)
	mux.HandleFunc("POST /v1/responses", responsesHandler(ctx, *flags.timeout, provider, issues, mekugiCalls, mentor))

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
		session := Session{BaseURL: baseURL, FrontendDirectory: frontendDirectory, GrokEnabled: *flags.grokEnabled, OpenCode: openCode, JournalEnabled: *flags.mode == "mekugi", PostCompactRecovery: *flags.postCompactRecovery, SkillsManagerAvailable: skillsManagerAvailable}
		if mekugiCalls != nil {
			session.StartUI = func(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File) (func() error, error) {
				return startTerminalUI(ctx, cmd, stdin, stdout, mekugiCalls.autoLiveDiff, mekugiCalls.replayStore, mekugiCalls.activity)
			}
			if mekugiCalls.nativeTrace != nil {
				session.NativeTraceDirectory = mekugiCalls.nativeTrace.directory
			}
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
		debug, requestID := debugRequest(request.Context())
		var upstreamStatus int
		var failure error
		defer func() {
			if tracked.statusCode >= 400 && request.Context().Err() == nil {
				if failure == nil {
					failure = staticCriticalDiagnostic(fmt.Sprintf("models_http_%d", upstreamStatus),
						fmt.Sprintf("the upstream model catalog returned HTTP %d", upstreamStatus))
				}
				finalization := &requestFinalization{sessionID: request.Header.Get(sessionIDHeader),
					threadID:     codexThreadID(request.Header),
					failurePhase: requestFailureModels, upstreamStatusCode: upstreamStatus,
					observation: requestObservation{outcome: requestOutcomeFailed}}
				issues.record(finalization, failure)
				issues.persistFailure(finalization)
				debug.event(map[string]any{
					"event": "models_request_failure", "request_id": requestID,
					"session_id": finalization.sessionID, "thread_id": codexThreadID(request.Header),
					"upstream_status": upstreamStatus, "downstream_status": tracked.statusCode,
					"diagnostic_code":      finalization.diagnosticCode,
					"diagnostic_reference": finalization.diagnosticReference,
					"error":                finalization.diagnosticError,
				})
			}
		}()

		response, err := provider.forwardModels(request.Context(), request.Header, request.URL.RawQuery)
		if err != nil {
			failure = modelsForwardDiagnostic(err)
			http.Error(writer, failure.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		upstreamStatus = response.StatusCode
		body, err := io.ReadAll(io.LimitReader(response.Body, modelsResponseBufferBytes+1))
		if err != nil {
			failure = criticalDiagnostic(err, "models_body_read", "the upstream model catalog response could not be read", true)
			http.Error(writer, fmt.Sprintf("read upstream models response: %v", err), http.StatusBadGateway)
			return
		}
		if len(body) > modelsResponseBufferBytes {
			failure = staticCriticalDiagnostic("models_body_limit", "the upstream model catalog response exceeded the router buffer budget")
			http.Error(writer, "upstream models response exceeds the router buffer budget", http.StatusBadGateway)
			return
		}
		if response.StatusCode >= http.StatusBadRequest {
			cause := fmt.Sprintf("model catalog upstream returned HTTP %d", response.StatusCode)
			if len(body) != 0 {
				cause += ": " + string(body)
			}
			failure = criticalDiagnostic(errors.New(cause), fmt.Sprintf("models_http_%d", response.StatusCode),
				fmt.Sprintf("the upstream model catalog returned HTTP %d", response.StatusCode), true)
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

func modelsForwardDiagnostic(err error) error {
	if _, classified := errors.AsType[*criticalDiagnosticError](err); classified {
		return err
	}
	diagnostic, _ := errors.AsType[*criticalDiagnosticError](forwardCriticalDiagnostic(err))
	code := diagnostic.code
	if !strings.HasPrefix(code, "models_") {
		code = "models_" + code
	}
	return &criticalDiagnosticError{err: err, code: code,
		summary: "the model catalog could not be fetched: " + diagnostic.summary, distinct: true}
}

func responsesHandler(
	lifecycle context.Context,
	responseStartTimeout time.Duration,
	provider responseProvider,
	issues *CriticalErrors,
	mekugiCalls *mekugiProxy,
	mentor *mentorHandoff,
) http.HandlerFunc {
	var serviceTiers map[string]string
	if client, ok := provider.(*providerClient); ok {
		serviceTiers = client.serviceTiers
	}
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
		executor := requestExecutor{provider: provider, output: trackedWriter, issues: issues, mekugiCalls: mekugiCalls, mentor: mentor, serviceTiers: serviceTiers}
		if err := executor.execute(startCtx, executionCtx, parsedRequest, request.Header, sessionID); err != nil {
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
		if rejection, ok := errors.AsType[*providerHTTPError](requestErr); ok {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(rejection.status)
			_, _ = writer.Write(mustMarshalJSON(map[string]any{"error": map[string]string{
				"type": "provider_error", "message": rejection.Error(),
			}}))
			return
		}
		http.Error(writer, requestErr.Error(), status)
	}
}

type requestFailurePhase string

const (
	requestFailurePrepare            requestFailurePhase = "prepare"
	requestFailureForward            requestFailurePhase = "forward"
	requestFailureModels             requestFailurePhase = "models"
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
	threadID              string
	turnID                string
	diagnosticMessage     string
	diagnosticError       string
	diagnosticNotice      *criticalNotice
	streamDiagnostics     *streamDiagnostics
	failurePhase          requestFailurePhase
	upstreamStatusCode    int
	providerFailure       error
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
		case errors.Is(requestErr, errDownstreamDisconnected),
			errors.Is(requestErr, context.Canceled) && errors.Is(ctx.Err(), context.Canceled):
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
	issues.persistFailure(f)

	return nil
}

func requestResponseStarted(output io.Writer) bool {
	switch writer := output.(type) {
	case *trackedResponseWriter:
		return writer.committed
	case *webSocketOutput:
		return writer.committed
	default:
		return false
	}
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
