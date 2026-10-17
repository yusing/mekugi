package router

import (
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
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

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

	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/httptransport"
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
	changehistory "github.com/yusing/mekugi/modules/changehistory/changes"
)

const (
	defaultListenAddress   = "127.0.0.1:0"
	requestBodyReadTimeout = 30 * time.Second
	shutdownTimeout        = 5 * time.Second
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
	var mekugiCalls *executionobservation.Preparer
	var registry *frontends.Registry
	var retained changehistory.Scope
	var changeSource changehistory.Service
	var checkpoints *changecapture.Workspaces
	var calls *history.CallCache
	var audience *sessioncontrol.Audience
	var review *sessionreview.Review
	var windows *executionobservation.Windows
	var nativeTrace *nativetrace.Reader
	var control sessioncontrol.Observations
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
		registry, err = frontends.New(ctx, frontends.Config{Commands: commands, Guidance: guidance, ReplayDirectory: replayDirectory, AXReadOutput: axReadOutput})
		if err != nil {
			return fmt.Errorf("initialize tool registry: %w", err)
		}
		defer func() {
			runErr = errors.Join(runErr, registry.Close())
		}()
		frontendDirectory = registry.FrontendDirectory
		retained.Store, err = service.Open(ctx, replayDirectory)
		if err != nil {
			return fmt.Errorf("initialize replay storage: %w", err)
		}
		checkpoints, err = changes.Checkpoints(retained.Store.Directory())
		if err != nil {
			return fmt.Errorf("initialize replay checkpoints: %w", err)
		}
		if changes != nil {
			changeSource = changes.Service
		}
		retained.Writes = writes
		retained.Limits = history.Limits{Total: history.DefaultStorageBytes, Kinds: map[string]int64{"commentary": 16 << 20}}
		if !options.DeduplicateOutput {
			projections = nil
		}
		calls = service.Calls()
		audience = sessioncontrol.NewAudience(func(category, message string) { issues.AddThreadNotice("", "", category, message) })
		windows = &executionobservation.Windows{}
		if titleGenerator != nil {
			titleGenerator.usage = usage
		}
		traceDirectory, traceErr := os.MkdirTemp("", "mekugi-native-trace-")
		if traceErr == nil {
			nativeTrace = nativetrace.New(traceDirectory)
			defer func() { runErr = errors.Join(runErr, os.RemoveAll(traceDirectory)) }()
		} else {
			issues.AddNotice("", "native_trace", "Nested tool confirmation unavailable: "+traceErr.Error())
		}
		review = sessionreview.New(ctx, live, func(category, message string) { issues.AddNotice("", category, message) })
		defer review.Close()
		mekugiCalls = executionobservation.New(executionobservation.Config{Registry: registry, InputProjections: slices.Clone(projections), Usage: usage, Review: review, Audience: audience, Windows: windows, NativeTrace: nativeTrace, Control: &control, Calls: calls, Retained: retained, Changes: changeSource, Checkpoints: checkpoints, Recovery: recovered, PublishChanges: review.Publish, Notice: issues.AddThreadNotice, PreviewTiming: previewbinding.Timing{Frame: previewview.PreviewFrameDelay, Unit: previewview.PreviewUnitDelay, Finish: previewview.PreviewFinishDrain}})
		usage.Configure(retained.Store.Directory(), retained.Writes, retained.Limits, func(thread string, err error) {
			issues.AddThreadNotice("", thread, "usage_storage", "Provider usage could not be retained or restored: "+err.Error())
		})
		if issues != nil {
			issues.Configure(requestexecution.FailureStorage{Diagnostics: diagnosis, Writes: writes, Store: retained.Store, Limits: retained.Limits})
		}
		defer func() {
			calls.StopAdmission()
			if reportObserver != nil {
				reportObserver.Close()
			}
			guardServer.Close()
			calls.Close()
			audience.Close()
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
					return retentionScope{store: retained.Store, writes: retained.Writes, limits: retained.Limits, thread: retained.Thread, notice: func(phase, message string) { issues.CleanupNotice("", "", phase, message) }}
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
	mux.HandleFunc("GET /v1/models", inferencegateway.ModelsHandler(provider, issues.ReportCatalog))
	inferencegateway.RegisterCodexAuxiliaryRoutes(mux, ctx, options.Timeout, provider)

	executor := requestexecution.Executor{Provider: provider, Client: provider, Issues: issues, ServiceTiers: serviceTiers, Naming: titleGenerator}
	if mekugiCalls != nil {
		executor.Observations, executor.Guidance = mekugiCalls, registry.Guidance
		executor.SkillsManager, executor.Control, executor.Usage = skillsManagerAvailable, &control, usage
	}
	webSocketEndpoint := inferencegateway.NewWebSocketEndpoint(ctx, options.Timeout, provider, executor.ExecuteWebSocket)
	defer webSocketEndpoint.Close()
	mux.Handle("GET /v1/responses", webSocketEndpoint)
	mux.HandleFunc("POST /v1/responses", inferencegateway.ResponsesHandler(ctx, options.Timeout, executor.ExecuteHTTP))

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
						windows.Reports = hub
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
			return startAppServerUI(terminalViews, changes, views, conversation, activity, service, prefs, segments.Reports, ctx, cmd, stdin, stdout, appServerIntegration{controlObservations: &control, usage: usage, audience: audience, review: review, execWindows: windows, nativeTrace: nativeTrace, retained: retained, changeSource: changeSource, snapshots: checkpoints, recovered: recovered, publishChanges: review.Publish, captureNotice: issues.AddThreadNotice, guard: guardServer, guards: guards, execTrack: reportObserver, skillsManager: skillsManagerAvailable}, issues, resumeThread, faint, serviceTiers, capture, resumeArgv, debugDirectory, titleGenerator, observations, approvals)
		}
		if mekugiCalls != nil && nativeTrace != nil {
			session.NativeTraceDirectory = nativeTrace.Directory()
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
