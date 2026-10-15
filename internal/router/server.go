package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/internal/sudoask"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	// Source: main.go:20:551 HTTP lifecycle and Responses proxy execution.
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"github.com/yusing/mekugi/internal/vcsguard"
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
	CodeModeEnabled        bool
	VCSGuard               bool
	GrokEnabled            bool
	GrokUnprefixed         bool
	ThirdPartyOnly         bool
	OpenCode               opencode.Config
	AXReadOutput           string
	SkillsManagerAvailable bool
	// StartAppUI starts the UI and returns its joined lifetime.
	// VCSGuard controls remote-write prompts independently of Codex policy.
	StartAppUI           func(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error)
	StartHeadless        func(context.Context, *exec.Cmd, io.Reader, io.Writer) (func() error, error)
	FrontendDirectory    string
	NativeTraceDirectory string
}

// RunSession owns the private router for one wrapped Codex process.
// artifacts receives retained debug paths at completion, even if startup was canceled.
func RunSession(service *history.Service, recovered *recovery.Service, segments *segmentservice.Service, changes changes.Service, ctx context.Context, commands []command.Contribution, options SessionConfig, issues *CriticalErrors, ready func(Session), artifacts func([]string)) (runErr error) {
	if options.mode == "" {
		return errors.New("session configuration has not been parsed")
	}
	grokEnabled, standalone := options.grok, options.standalone
	faint, err := terminalui.SupportsFaint(ctx, options.ansiFaint)
	if err != nil {
		return err
	}
	config, err := loadMekugiConfig(standalone)
	if err != nil {
		return err
	}
	openCode := config.Providers
	if err := validateAXOutputAliases(os.Getenv(capturer.AXReadOutputEnvironment), options.captureOutput); err != nil {
		return err
	}
	debug, err := openDebugOutput(service, options.debug, options.captureOutput)
	if err != nil {
		return err
	}
	if debug != nil {
		ctx = context.WithValue(ctx, debugContextKey{}, debug)
	}
	var mekugiCalls *mekugiProxy
	defer func() {
		if debug != nil {
			debug.event(map[string]any{"event": "router_stop", "failed": runErr != nil, "storage_writes": debug.writes.Snapshot()})
			runErr = errors.Join(runErr, debug.close())
		}
		if artifacts != nil {
			var paths []string
			if debug != nil {
				paths = append(paths, debug.paths...)
			}
			if len(paths) != 0 {
				artifacts(paths)
			}
		}
	}()
	var writes *persistence.Counter
	if debug != nil {
		writes = debug.writes
	}
	capturePath := options.captureOutput
	if debug != nil {
		capturePath = debug.paths[1]
	}
	capture, err := capturer.New(capturer.Config{Output: capturePath, Mode: options.mode, Writes: writes})
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
			runErr = errors.Join(runErr, capture.WriteMetrics(writes.Writer(metricsFile)), metricsFile.Close())
		}
		runErr = errors.Join(runErr, capture.Close())
	}()
	provider := newProviderClient(codexBaseURL, nil)
	provider.thirdPartyOnly = grokEnabled || standalone
	provider.httpClient.Transport = capture.Transport(provider.httpClient.Transport)
	provider.streamIdleTimeout = options.streamIdleTimeout
	provider.enableWebSockets(ctx)
	// Cover early setup returns before ctx is canceled; close is idempotent
	// with enableWebSockets' normal context-shutdown callback.
	defer provider.websockets.close()
	if grokEnabled || standalone {
		apiKey := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		path := options.grokAuthFile
		if path == "" && apiKey == "" {
			home, err := os.UserHomeDir()
			if err != nil && !standalone {
				return fmt.Errorf("locate Grok credentials: %w", err)
			}
			if err == nil {
				path = filepath.Join(home, ".grok", "auth.json")
			}
		}
		auth := grok.NewAuth(path, apiKey, withDialTimeout(nil))
		if _, authErr := auth.Credentials(ctx); authErr == nil {
			client := withDialTimeout(nil)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			client.Transport = capture.Transport(client.Transport)
			provider.grok = &grokClient{httpClient: client, auth: auth, unprefixed: grokEnabled, streamIdleTimeout: options.streamIdleTimeout}
		} else if grokEnabled {
			return authErr
		}
	}
	if standalone && provider.grok == nil && !openCode.Enabled() {
		return errors.New("no authenticated third-party providers; run grok login --oauth or set XAI_API_KEY, OPENCODE_GO_API_KEY, or OPENCODE_ZEN_API_KEY")
	}
	if openCode.Enabled() {
		cachePath := ""
		if directory, err := os.UserCacheDir(); err == nil {
			cachePath = filepath.Join(directory, "mekugi", "opencode-models-v2.json")
		}
		openCode.Catalog = opencode.NewCatalog(cachePath, nil)
		if err := openCode.Catalog.Refresh(ctx, false); err != nil {
			log.Printf("OpenCode catalog: refresh/cache update unavailable; retaining last usable metadata")
		}
	}
	provider.serviceTiers = &serviceTierSettings{configured: config.ServiceTiers}
	provider.opencode = make(map[string]*grokClient)
	for _, service := range openCode.Services() {
		client := withDialTimeout(nil)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client.Transport = capture.Transport(client.Transport)
		provider.opencode[service.Prefix] = &grokClient{httpClient: client, openCode: &service, streamIdleTimeout: options.streamIdleTimeout}
	}
	var frontendDirectory string
	titleCtx, cancelTitles := context.WithCancel(ctx)
	defer cancelTitles()
	provider.titleGenerator = newSessionTitleGenerator(titleCtx, provider)
	if issues != nil {
		issues.persistFailures = true
		issues.history = service
		issues.writes = writes
	}
	if options.mode == "mekugi" {
		registry, err := buildToolRegistry(ctx, commands)
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
		replayStore, err := openReplayStore(ctx, service, replayDirectory)
		if replayStore != nil {
			replayStore.recovery, replayStore.changes = recovered, changes
		}
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		replayStore.writes = writes
		defer replayStore.snapshots.close()
		mekugiCalls = newMekugiProxy(registry)
		provider.titleGenerator.usage = mekugiCalls.usage
		mekugiCalls.duplicateOutput = options.duplicateOutput
		traceDirectory, traceErr := os.MkdirTemp("", "mekugi-native-trace-")
		if traceErr == nil {
			mekugiCalls.nativeTrace = &nativeToolTrace{directory: traceDirectory}
			defer func() { runErr = errors.Join(runErr, os.RemoveAll(traceDirectory)) }()
		} else {
			issues.addNotice("", "native_trace", "Nested tool confirmation unavailable: "+traceErr.Error())
		}
		mekugiCalls.noticeSink = issues.addThreadNotice
		replayStore.storageNotice = func(session, thread, phase, message string) {
			issues.addThreadNotice(session, thread, "storage_cleanup_"+phase, message)
		}
		var stopLiveDiff func()
		mekugiCalls.autoLiveDiff, stopLiveDiff = newAutoLiveDiff(ctx)
		mekugiCalls.autoLiveDiff.notice = func(category, message string) { issues.addNotice("", category, message) }
		replayStore.liveDiff = mekugiCalls.autoLiveDiff.events.publish
		defer stopLiveDiff()
		mekugiCalls.replayStore = replayStore
		mekugiCalls.usage.store = replayStore
		mekugiCalls.usage.notice = func(thread string, err error) {
			mekugiCalls.notice("", thread, "usage_storage", "Provider usage could not be retained or restored: "+err.Error())
		}
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
	if mekugiCalls != nil || issues != nil {
		retentionCtx, stopRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			defer close(retentionDone)
			runStorageRetention(retentionCtx, func() *mekugiReplayStore {
				if mekugiCalls != nil {
					return mekugiCalls.replayStore
				}
				issues.mu.Lock()
				defer issues.mu.Unlock()
				return issues.failureStore
			}, func(err error) {
				issues.addNotice("", "storage_cleanup_failure", fmt.Sprintf("Mekugi could not complete background storage cleanup: %v. Session requests continue unless storage reaches its limit.", err))
			})
		}()
		defer func() {
			stopRetention()
			<-retentionDone
		}()
	}

	stopProfiling, err := startProfiling(ctx, os.Stderr)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, stopProfiling()) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/metrics", capture.ServeHTTP)
	mux.HandleFunc("GET /v1/models", modelsHandler(provider, issues))
	registerCodexAuxiliaryRoutes(mux, ctx, options.timeout, provider)

	webSocketEndpoint := responsesWebSocketHandler(ctx, options.timeout, provider, issues, mekugiCalls)
	defer webSocketEndpoint.Close()
	mux.Handle("GET /v1/responses", webSocketEndpoint)
	mux.HandleFunc("POST /v1/responses", responsesHandler(ctx, options.timeout, provider, issues, mekugiCalls))

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
		session := Session{BaseURL: baseURL, FrontendDirectory: frontendDirectory, GrokEnabled: provider.grok != nil, GrokUnprefixed: grokEnabled, ThirdPartyOnly: provider.thirdPartyOnly, OpenCode: openCode, CodeModeEnabled: options.mode == "mekugi", VCSGuard: options.vcsGuard, SkillsManagerAvailable: skillsManagerAvailable}
		if mekugiCalls != nil {
			session.StartHeadless = func(ctx context.Context, cmd *exec.Cmd, input io.Reader, output io.Writer) (func() error, error) {
				return startHeadlessAppServer(ctx, cmd, input, output, mekugiCalls, issues)
			}
		}
		session.StartAppUI = func(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error) {
			if mekugiCalls != nil && frontendDirectory != "" {
				// Without the socket, command shells find no router and run
				// their scripts untracked. Without the approval channel,
				// guarded remote writes are denied, so guarded startup stops.
				err := segments.LiveError
				var hub *execTrackHub
				if segments.Live != nil {
					guard, channel := vcsguard.Paths(frontendDirectory)
					mekugiCalls.guard, err = listenShellApprovals(ctx, channel, vcsguard.Timeout)
					if err == nil {
						hub, err = attachExecTrack(segments.Live, execInstrumentation{vcs: guard, sudo: sudoask.Path(frontendDirectory)})
					}
					if err != nil {
						segments.Live.Close()
						mekugiCalls.guard.close()
					}
				} else if err == nil {
					err = errors.New("command reporting is unavailable")
				}

				if err == nil {
					mekugiCalls.execTrack = hub
					mekugiCalls.execWindows.tracker = hub
				} else if session.VCSGuard {
					return nil, fmt.Errorf("listen for guarded VCS writes: %w", err)
				}
			}
			debugDirectory := ""
			if debug != nil {
				debugDirectory = filepath.Dir(debug.paths[0])
			}
			return startAppServerUI(service, segments.Reports, ctx, cmd, stdin, stdout, mekugiCalls, issues, resumeThread, faint, provider.serviceTiers, capture, resumeArgv, debugDirectory, provider.titleGenerator, approvals)
		}
		if mekugiCalls != nil && mekugiCalls.nativeTrace != nil {
			session.NativeTraceDirectory = mekugiCalls.nativeTrace.directory
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
	// Session-local, single-entry cache. The gate coalesces concurrent refreshes
	// without keeping canceled callers waiting for an upstream operation.
	gate := make(chan struct{}, 1)
	var cached *modelsCacheEntry
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

		release := func() {}
		key, cacheable := modelsRequestCacheKey(request)
		if cacheable {
			select {
			case gate <- struct{}{}:
				release = sync.OnceFunc(func() { <-gate })
				defer release()
			case <-request.Context().Done():
				return
			}
			if request.Context().Err() != nil {
				return
			}
			if cached != nil && cached.key == key {
				entry := cached
				release()
				writeModelsResponse(writer, http.StatusOK, entry.headers, entry.body)
				return
			}
		}

		response, err := provider.forwardModels(request.Context(), request.Header, request.URL.RawQuery)
		if err != nil {
			failure = modelsForwardDiagnostic(err)
			release()
			http.Error(writer, failure.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		upstreamStatus = response.StatusCode
		body, err := io.ReadAll(io.LimitReader(response.Body, modelsResponseBufferBytes+1))
		if err != nil {
			failure = criticalDiagnostic(err, "models_body_read", "the upstream model catalog response could not be read", true)
			release()
			http.Error(writer, fmt.Sprintf("read upstream models response: %v", err), http.StatusBadGateway)
			return
		}
		if len(body) > modelsResponseBufferBytes {
			failure = staticCriticalDiagnostic("models_body_limit", "the upstream model catalog response exceeded the router buffer budget")
			release()
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
		if cacheable && response.StatusCode == http.StatusOK {
			headers := make(http.Header)
			for _, name := range []string{"Content-Type", "Cache-Control", "ETag"} {
				for _, value := range response.Header.Values(name) {
					headers.Add(name, value)
				}
			}
			cached = &modelsCacheEntry{key: key, headers: headers, body: body}
		}
		release()
		writeModelsResponse(writer, response.StatusCode, response.Header, body)
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
) http.HandlerFunc {
	var serviceTiers *serviceTierSettings
	var titleGenerator *sessionTitleGenerator
	if client, ok := provider.(*providerClient); ok {
		serviceTiers = client.serviceTiers
		titleGenerator = client.titleGenerator
	}
	return func(writer http.ResponseWriter, request *http.Request) {
		trackedWriter := &trackedResponseWriter{ResponseWriter: writer}
		body, status, err := readCodexHTTPBody(request)
		if err != nil {
			http.Error(trackedWriter, err.Error(), status)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			http.Error(trackedWriter, "responses request body is empty", http.StatusBadRequest)
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
		executor := requestExecutor{provider: provider, output: trackedWriter, issues: issues, mekugiCalls: mekugiCalls, serviceTiers: serviceTiers, titleGenerator: titleGenerator}
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
	n, err := w.ResponseWriter.Write(body)
	return n, downstreamDisconnectError(err)
}

func (w *trackedResponseWriter) FlushError() error {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return downstreamDisconnectError(http.NewResponseController(w.ResponseWriter).Flush())
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
			_, _ = writer.Write(responses.MustMarshalJSON(map[string]any{"error": map[string]string{
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
) {
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
