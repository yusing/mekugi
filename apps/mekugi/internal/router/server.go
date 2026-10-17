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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/backends/codex/nativetrace"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/usage/accounting"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/openai"
	"github.com/yusing/mekugi/providers/opencode"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	// Source: main.go:20:551 HTTP lifecycle and Responses proxy execution.

	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

const (
	defaultListenAddress      = "127.0.0.1:0"
	requestBodyReadTimeout    = 30 * time.Second
	shutdownTimeout           = 5 * time.Second
	modelsResponseBufferBytes = 8 << 20
)

// Session is available only after initialization and listener binding succeed.
type Session struct {
	BaseURL                string
	CodeModeEnabled        bool
	Guards                 []binding.ShellApproval
	GrokEnabled            bool
	GrokUnprefixed         bool
	ThirdPartyOnly         bool
	OpenCode               opencode.Config
	AXReadOutput           string
	SkillsManagerAvailable bool
	// StartAppUI starts the UI and returns its joined lifetime.
	// Guards contains only the enabled, composed approval modules.
	StartAppUI           func(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error)
	FrontendDirectory    string
	NativeTraceDirectory string
}

// RunSession owns the private router for one wrapped Codex process.
// artifacts receives retained debug paths at completion, even if startup was canceled.
func RunSession(terminalViews terminalsession.Views, views *sessionpresentation.Service, conversation *conversationmodel.Service, live *previewmodel.Service, activity *activitymodel.Service, service *history.Service, prefs *preferences.Service, recovered *recovery.Service, segments *segmentservice.Service, changes *changecapture.Service, usage *accounting.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, diagnosis *diagnostic.Service, ctx context.Context, commands []command.Contribution, projections []binding.InputProjection, guidance *binding.Guidance, naming *binding.SessionNaming, observations *binding.Observation, guards []binding.ShellApproval, options launchoptions.Config, issues *requestexecution.Failures, ready func(Session), artifacts func([]string)) (runErr error) {
	if diagnostics == nil && options.CaptureOutput != "" {
		return errors.New("transport diagnostics are unavailable")
	}
	if options.Mode == "" {
		return errors.New("session configuration has not been parsed")
	}
	if !options.ShellGuard {
		guards = nil
	}
	grokEnabled, standalone := options.Grok, options.ThirdParty
	faint, err := terminalui.SupportsFaint(ctx, options.ANSIFaint)
	if err != nil {
		return err
	}
	config, err := launchoptions.LoadSettings(standalone)
	if err != nil {
		return err
	}
	openCode := config.Providers
	if agentMetrics != nil {
		if err := evidence.ValidateOutputAliases(os.Getenv(evidence.AXReadOutputEnvironment), options.CaptureOutput); err != nil {
			return err
		}
	}
	debug, err := openDebugOutput(diagnosis, options.Debug, options.CaptureOutput)
	if err != nil {
		return err
	}
	if debug != nil {
		ctx = requestexecution.WithDebug(ctx, debug)
	}
	var mekugiCalls *mekugiProxy
	var guardServer *sessioncontrol.ShellApprovals
	var reportObserver *segmentservice.Observer
	defer func() {
		if debug != nil {
			debug.Event(map[string]any{"event": "router_stop", "failed": runErr != nil, "storage_writes": debug.Writes().Snapshot()})
			runErr = errors.Join(runErr, debug.Close())
		}
		if artifacts != nil {
			var paths []string
			if debug != nil {
				paths = append(paths, debug.Paths()...)
			}
			if len(paths) != 0 {
				artifacts(paths)
			}
		}
	}()
	var writes *persistence.Counter
	if debug != nil {
		writes = debug.Writes()
	}
	capturePath := options.CaptureOutput
	if debug != nil {
		capturePath = debug.CapturePath()
	}
	var capture *transportcapture.Recorder
	if diagnostics != nil {
		capture, err = diagnostics.Open(transportcapture.Config{Output: capturePath, Mode: options.Mode, Writes: writes})
		if err != nil {
			return fmt.Errorf("initialize capture: %w", err)
		}
	}
	var metricsFile *os.File
	if debug != nil && capture != nil {
		metricsFile, err = os.OpenFile(debug.MetricsPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return errors.Join(fmt.Errorf("open metrics output: %w", err), capture.Close())
		}
	}
	defer func() {
		if metricsFile != nil {
			runErr = errors.Join(runErr, capture.WriteMetrics(writes.Writer(metricsFile)), metricsFile.Close())
		}
		if capture != nil {
			runErr = errors.Join(runErr, capture.Close())
		}
	}()
	openaiHTTP := httptransport.Client(nil)
	if capture != nil {
		openaiHTTP.Transport = capture.Transport(openaiHTTP.Transport)
	}
	provider := inferencegateway.NewClient(openai.BaseURL, openaiHTTP, requestexecution.ProviderDiagnostics())
	provider.ThirdPartyOnly = grokEnabled || standalone
	provider.OpenAI.StreamIdleTimeout = options.StreamIdleTimeout
	provider.OpenAI.EnableWebSockets(ctx)
	// Cover early setup returns before ctx is canceled; close is idempotent
	// with enableWebSockets' normal context-shutdown callback.
	defer provider.OpenAI.Close()
	if grokEnabled || standalone {
		apiKey := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		path := options.GrokAuthFile
		if path == "" && apiKey == "" {
			home, err := os.UserHomeDir()
			if err != nil && !standalone {
				return fmt.Errorf("locate Grok credentials: %w", err)
			}
			if err == nil {
				path = filepath.Join(home, ".grok", "auth.json")
			}
		}
		auth := grok.NewAuth(path, apiKey, httptransport.Client(nil))
		if _, authErr := auth.Credentials(ctx); authErr == nil {
			client := httptransport.Client(nil)
			if capture != nil {
				client.Transport = capture.Transport(client.Transport)
			}
			provider.Grok = &inferencegateway.ThirdPartyClient{HTTPClient: client, Auth: auth, Unprefixed: grokEnabled, StreamIdleTimeout: options.StreamIdleTimeout, Diagnostics: provider.Diagnostics}
		} else if grokEnabled {
			return authErr
		}
	}
	if standalone && provider.Grok == nil && !openCode.Enabled() {
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
	serviceTiers := inferencegateway.NewTiers(config.ServiceTiers)
	provider.OpenCode = make(map[string]*inferencegateway.ThirdPartyClient)
	for _, service := range openCode.Services() {
		client := httptransport.Client(nil)
		if capture != nil {
			client.Transport = capture.Transport(client.Transport)
		}
		provider.OpenCode[service.Prefix] = &inferencegateway.ThirdPartyClient{HTTPClient: client, OpenCode: &service, StreamIdleTimeout: options.StreamIdleTimeout, Diagnostics: provider.Diagnostics}
	}
	var frontendDirectory string
	titleCtx, cancelTitles := context.WithCancel(ctx)
	defer cancelTitles()
	titleGenerator := newSessionNaming(titleCtx, provider, naming)
	if issues != nil {
		issues.Configure(requestexecution.FailureStorage{Diagnostics: diagnosis, Writes: writes})
	}
	if options.Mode == "mekugi" {
		replayDirectory, err := history.ReplayDirectory()
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		var axReadOutput string
		if debug != nil {
			axReadOutput = debug.ReadLogPath()
		}
		registry, err := frontends.New(ctx, frontends.Config{Commands: commands, Guidance: guidance, ReplayDirectory: replayDirectory, AXReadOutput: axReadOutput})
		if err != nil {
			return fmt.Errorf("initialize tool registry: %w", err)
		}
		defer func() {
			runErr = errors.Join(runErr, registry.Close())
		}()
		frontendDirectory = registry.FrontendDirectory
		replayStore, err := openReplayStore(ctx, service, changes, replayDirectory)
		if replayStore != nil {
			replayStore.recovery = recovered
		}
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		replayStore.writes = writes
		if !options.DeduplicateOutput {
			projections = nil
		}
		mekugiCalls = newMekugiProxy(registry, projections, usage, service)
		if titleGenerator != nil {
			titleGenerator.usage = mekugiCalls.usage
		}
		traceDirectory, traceErr := os.MkdirTemp("", "mekugi-native-trace-")
		if traceErr == nil {
			mekugiCalls.nativeTrace = nativetrace.New(traceDirectory)
			defer func() { runErr = errors.Join(runErr, os.RemoveAll(traceDirectory)) }()
		} else {
			issues.AddNotice("", "native_trace", "Nested tool confirmation unavailable: "+traceErr.Error())
		}
		mekugiCalls.noticeSink = issues.AddThreadNotice
		replayStore.storageNotice = func(session, thread, phase, message string) {
			issues.CleanupNotice(session, thread, phase, message)
		}
		mekugiCalls.review = sessionreview.New(ctx, live, func(category, message string) { issues.AddNotice("", category, message) })
		defer mekugiCalls.review.Close()
		replayStore.liveDiff = mekugiCalls.review.Publish
		mekugiCalls.replayStore = replayStore
		mekugiCalls.usage.Configure(replayStore.directory, replayStore.writes, replayStore.retentionLimits(), func(thread string, err error) {
			issues.AddThreadNotice("", thread, "usage_storage", "Provider usage could not be retained or restored: "+err.Error())
		})
		if issues != nil {
			issues.Configure(requestexecution.FailureStorage{Diagnostics: diagnosis, Writes: writes, Store: replayStore.publication, Limits: replayStore.retentionLimits()})
		}
		defer func() {
			mekugiCalls.calls.StopAdmission()
			if reportObserver != nil {
				reportObserver.Close()
			}
			guardServer.Close()
			mekugiCalls.calls.Close()
			mekugiCalls.activity.Close()
		}()
	}
	skillsManagerAvailable := mekugiCalls != nil && skillsManagerInPath(frontendDirectory)

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
			runStorageRetention(retentionCtx, diagnosis, func() retentionScope {
				if mekugiCalls != nil {
					return mekugiCalls.replayStore.retentionScope(retentionCtx)
				}
				storage := issues.Storage()
				return retentionScope{store: storage.Store, writes: storage.Writes, limits: storage.Limits, notice: func(phase, message string) { issues.CleanupNotice("", "", phase, message) }}
			}, func(err error) {
				issues.AddNotice("", "storage_cleanup_failure", fmt.Sprintf("Mekugi could not complete background storage cleanup: %v. Session requests continue unless storage reaches its limit.", err))
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
	if capture != nil {
		mux.HandleFunc("GET /api/metrics", capture.ServeHTTP)
	}
	mux.HandleFunc("GET /v1/models", modelsHandler(provider, issues))
	registerCodexAuxiliaryRoutes(mux, ctx, options.Timeout, provider)

	executor := requestexecution.Executor{Provider: provider, Client: provider, Issues: issues, ServiceTiers: serviceTiers, Naming: titleGenerator}
	if mekugiCalls != nil {
		executor.Observations, executor.Guidance = mekugiCalls, mekugiCalls.registry.Guidance
		executor.SkillsManager, executor.Control, executor.Usage = skillsManagerAvailable, &mekugiCalls.control, mekugiCalls.usage
	}
	webSocketEndpoint := inferencegateway.NewWebSocketEndpoint(ctx, options.Timeout, provider, executor.ExecuteWebSocket)
	defer webSocketEndpoint.Close()
	mux.Handle("GET /v1/responses", webSocketEndpoint)
	mux.HandleFunc("POST /v1/responses", responsesHandler(ctx, options.Timeout, executor))

	var handler http.Handler = debug.Handler(mux)
	if capture != nil {
		handler = capture.Handler(handler)
	}
	server := &http.Server{
		ErrorLog:          log.New(io.Discard, "", 0), // Disable net/http terminal diagnostics while Codex owns it.
		Addr:              defaultListenAddress,
		Handler:           handler,
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
		session := Session{BaseURL: baseURL, FrontendDirectory: frontendDirectory, GrokEnabled: provider.Grok != nil, GrokUnprefixed: grokEnabled, ThirdPartyOnly: provider.ThirdPartyOnly, OpenCode: openCode, CodeModeEnabled: options.Mode == "mekugi", Guards: guards, SkillsManagerAvailable: skillsManagerAvailable}
		session.StartAppUI = func(ctx context.Context, cmd *exec.Cmd, stdin, stdout *os.File, resumeThread string, resumeArgv []string, approvals bool) (func() error, error) {
			if mekugiCalls != nil && frontendDirectory != "" {
				instrumentation := segmentservice.Instrumentation{}
				if len(session.Guards) != 0 {
					guard, channel := shellapproval.Paths(frontendDirectory)
					var approvalErr error
					guardServer, approvalErr = sessioncontrol.ListenShellApprovals(ctx, channel, shellapproval.Timeout, guards)
					if approvalErr != nil {
						return nil, fmt.Errorf("listen for guarded shell commands: %w", approvalErr)
					}
					for _, registration := range guards {
						switch registration.Kind {
						case binding.RemoteWrite:
							instrumentation.VCS = guard
						case binding.Sudo:
							instrumentation.Sudo = shellapproval.SudoDirectory(frontendDirectory)
						}
					}
				}
				// Reporting is auxiliary; acquisition or attachment failure
				// cannot close the independent command approval channel.
				reportErr := segments.LiveError
				if segments.Live != nil {
					var hub *segmentservice.Observer
					hub, reportErr = segmentservice.NewObserver(segments.Live, instrumentation, activitymodel.OutputBytes)
					if reportErr == nil {
						reportObserver = hub
						mekugiCalls.execWindows.tracker = hub
					} else {
						segments.Live.Close()
					}
				}
				if reportErr != nil {
					issues.AddNotice("", "command_reporting_unavailable", "Command segment tracking is unavailable. Commands retain their original output and exit status: "+reportErr.Error())
				}
			}
			debugDirectory := ""
			if debug != nil {
				debugDirectory = filepath.Dir(debug.Paths()[0])
			}
			return startAppServerUI(terminalViews, changes, views, conversation, activity, service, prefs, segments.Reports, ctx, cmd, stdin, stdout, appServerIntegration{controlObservations: &mekugiCalls.control, usage: mekugiCalls.usage, audience: mekugiCalls.activity, review: mekugiCalls.review, execWindows: mekugiCalls.execWindows, nativeTrace: mekugiCalls.nativeTrace, retained: mekugiCalls.replayStore, captureNotice: mekugiCalls.noticeSink, guard: guardServer, guards: guards, execTrack: reportObserver, skillsManager: skillsManagerAvailable}, issues, resumeThread, faint, serviceTiers, capture, resumeArgv, debugDirectory, titleGenerator, observations, approvals)
		}
		if mekugiCalls != nil && mekugiCalls.nativeTrace != nil {
			session.NativeTraceDirectory = mekugiCalls.nativeTrace.Directory()
		}
		if debug != nil {
			session.AXReadOutput = debug.ReadLogPath()
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

func modelsHandler(provider *inferencegateway.Client, issues *requestexecution.Failures) http.HandlerFunc {
	// Session-local, single-entry cache. The gate coalesces concurrent refreshes
	// without keeping canceled callers waiting for an upstream operation.
	gate := make(chan struct{}, 1)
	var cached *modelsCacheEntry
	return func(writer http.ResponseWriter, request *http.Request) {
		tracked := &inferencegateway.ResponseWriter{ResponseWriter: writer}
		writer = tracked
		debug, requestID := requestexecution.DebugRequest(request.Context())
		var upstreamStatus int
		var failure error
		defer func() {
			if tracked.StatusCode() >= 400 && request.Context().Err() == nil {
				if failure == nil {
					failure = requestexecution.StaticError(fmt.Sprintf("models_http_%d", upstreamStatus),
						fmt.Sprintf("the upstream model catalog returned HTTP %d", upstreamStatus))
				}
				result := issues.Report(requestexecution.Failure{Session: request.Header.Get(codexresponses.RoutingSessionHeader), Thread: codexresponses.ThreadID(request.Header), Phase: "models", UpstreamStatus: upstreamStatus}, failure)
				if debug != nil {
					debug.Event(map[string]any{
						"event": "models_request_failure", "request_id": requestID,
						"session_id": request.Header.Get(codexresponses.RoutingSessionHeader), "thread_id": codexresponses.ThreadID(request.Header),
						"upstream_status": upstreamStatus, "downstream_status": tracked.StatusCode(),
						"diagnostic_code":      result.Code,
						"diagnostic_reference": result.Reference,
						"error":                result.Error,
					})
				}
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

		response, err := provider.ForwardModels(request.Context(), request.Header, request.URL.RawQuery)
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
			failure = requestexecution.Classify(err, "models_body_read", "the upstream model catalog response could not be read", true)
			release()
			http.Error(writer, fmt.Sprintf("read upstream models response: %v", err), http.StatusBadGateway)
			return
		}
		if len(body) > modelsResponseBufferBytes {
			failure = requestexecution.StaticError("models_body_limit", "the upstream model catalog response exceeded the router buffer budget")
			release()
			http.Error(writer, "upstream models response exceeds the router buffer budget", http.StatusBadGateway)
			return
		}
		if response.StatusCode >= http.StatusBadRequest {
			cause := fmt.Sprintf("model catalog upstream returned HTTP %d", response.StatusCode)
			if len(body) != 0 {
				cause += ": " + string(body)
			}
			failure = requestexecution.Classify(errors.New(cause), fmt.Sprintf("models_http_%d", response.StatusCode),
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
	if _, invalid := errors.AsType[*inferencegateway.ModelCredentialsError](err); invalid {
		return requestexecution.Classify(err, "models_auth_headers", "the model catalog request is missing valid Codex Authorization or account headers", true)
	}
	if _, classified := errors.AsType[*requestexecution.DiagnosticError](err); classified {
		return err
	}
	diagnostic, _ := errors.AsType[*requestexecution.DiagnosticError](requestexecution.ForwardError(err))
	code := diagnostic.Code
	if !strings.HasPrefix(code, "models_") {
		code = "models_" + code
	}
	return &requestexecution.DiagnosticError{Cause: err, Code: code,
		Summary: "the model catalog could not be fetched: " + diagnostic.Summary, Distinct: true}
}

func responsesHandler(lifecycle context.Context, responseStartTimeout time.Duration, executor requestexecution.Executor) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		trackedWriter := &inferencegateway.ResponseWriter{ResponseWriter: writer}
		body, status, err := readCodexHTTPBody(request)
		if err != nil {
			http.Error(trackedWriter, err.Error(), status)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			http.Error(trackedWriter, "responses request body is empty", http.StatusBadRequest)
			return
		}
		parsedRequest, err := codexresponses.ParseRequest(body)
		if err != nil {
			http.Error(trackedWriter, err.Error(), http.StatusBadRequest)
			return
		}
		startCtx, executionCtx, cancelRequest := inferencegateway.RequestContexts(request.Context(), lifecycle, responseStartTimeout)
		defer cancelRequest()
		sessionID := codexresponses.RoutingSessionID(request.Header, parsedRequest)
		executor := executor
		executor.Output = trackedWriter
		if err := executor.Execute(startCtx, executionCtx, parsedRequest, request.Header, sessionID); err != nil {
			writeRequestError(trackedWriter, err)
		}
	}
}

func writeRequestError(writer *inferencegateway.ResponseWriter, requestErr error) {
	if !writer.ResponseStarted() {
		status := http.StatusBadGateway
		if _, ok := errors.AsType[*inferencegateway.CompatibilityError](requestErr); ok {
			status = http.StatusBadRequest
		}
		if rejection, ok := errors.AsType[*inferencegateway.ProviderHTTPError](requestErr); ok {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(rejection.Status)
			_, _ = writer.Write(responses.MustMarshalJSON(map[string]any{"error": map[string]string{
				"type": "provider_error", "message": rejection.Error(),
			}}))
			return
		}
		http.Error(writer, requestErr.Error(), status)
	}
}
