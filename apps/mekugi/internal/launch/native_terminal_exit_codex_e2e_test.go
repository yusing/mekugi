//go:build codex_e2e

package launch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"golang.org/x/term"
)

type terminalExitProvider struct {
	mu       sync.Mutex
	main     string
	calls    int
	delegate bool
	active   chan string
	finish   chan struct{}
}

func (p *terminalExitProvider) ForwardExecution(_, ctx context.Context, _ []byte, headers http.Header, _ string) (*inferencegateway.Response, error) {
	metadata, _ := codexresponses.DecodeTurnMetadata(headers)
	thread := headers.Get(codexresponses.ThreadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	p.mu.Lock()
	if p.main == "" {
		p.main = thread
	}
	main := thread == p.main
	p.calls++
	first := p.calls == 1
	p.mu.Unlock()
	if p.delegate && main {
		if first {
			return nativeCollabResponse(`text(await tools.collaboration__spawn_agent({task_name:"worker",message:"Finish the exit fixture work.",fork_turns:"none"}));`, "")
		}
		return nativeCollabResponse("", "Main finished before the exit choice.")
	}
	select {
	case p.active <- thread:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.finish:
		return nativeCollabResponse("", "Retained answer from background work.")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestNativeTerminalExitCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"idle", "stop", "finish-before-stop", "deferred-release", "resume-live"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			p := &terminalExitProvider{
				delegate: mode == "deferred-release" || mode == "resume-live",
				active:   make(chan string, 1), finish: make(chan struct{}),
			}
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			provider := httptest.NewServer(testResponsesHandler(ctx, time.Minute, p, nil, proxy))
			defer provider.Close()
			defer cancel()
			client, workspace, owners, view := newTerminalExitTestServer(t, ctx, codex, provider.URL, testAppServerIntegration(proxy))
			connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{Workspace: workspace})
			if err != nil {
				t.Fatal(err)
			}
			terminal := view(connection)
			terminal.await("Ready")
			if mode == "idle" {
				waitForTerminalExit(t, terminal)
				requireTerminalOwnerExit(t, ctx, owners)
				return
			}
			terminal.send("Exercise attached exit.\r")
			if p.delegate {
				terminal.await("Main finished before the exit choice.")
				terminal.await("Completed in")
			}
			var active string
			select {
			case active = <-p.active:
			case <-time.After(5 * time.Second):
				t.Fatalf("host did not start work\n%s", terminal.screen.String())
			}
			terminal.send("/quit\r")
			terminal.await("Continue in background")
			if !strings.Contains(terminal.screen.String(), "Stop") {
				t.Fatal("exit did not offer both choices")
			}
			terminal.send("\x1b")
			terminal.awaitMatch("exit choice dismissed", func(screen string) bool { return !strings.Contains(screen, "Continue in background") })
			terminal.send("/quit\r")
			terminal.await("Continue in background")
			p.mu.Lock()
			main := p.main
			p.mu.Unlock()
			if main == "" {
				t.Fatal("provider did not identify Main")
			}

			if mode == "stop" || mode == "finish-before-stop" {
				if mode == "finish-before-stop" {
					close(p.finish)
					terminal.await("Retained answer from background work.")
					terminal.await("main  done")
				}
				terminal.send("\x1b[B\r")
			} else {
				terminal.send("\r") // Continue in background is selected first.
			}
			select {
			case err := <-terminal.done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("terminal did not release:", ctx.Err())
			}
			terminal.checkRestored()

			if mode == "resume-live" {
				live, err := sessionapi.OpenNative(ctx, client, "http://mekugi", main)
				if err != nil {
					t.Fatal("resume did not reconnect during delegate work:", err)
				}
				reattached := view(live)
				reattached.await("Main finished before the exit choice.")
				close(p.finish)
				reattached.await("worker  done")
				child, err := sessionapi.OpenNative(ctx, client, "http://mekugi", active)
				if err != nil {
					t.Fatal("could not attach completed delegate:", err)
				}
				childTerminal := view(child)
				childTerminal.await("Retained answer from background work.")
				childTerminal.await("Read-only")
				waitForTerminalExit(t, childTerminal)
				select {
				case err := <-owners:
					t.Fatalf("delegate exit affected its live Main: %v", err)
				default:
				}
				return
			}
			if mode == "deferred-release" {
				select {
				case err := <-owners:
					t.Fatalf("owner ended before its delegate finished: %v", err)
				default:
				}
				close(p.finish)
			}
			requireTerminalOwnerExit(t, ctx, owners)
			serverCLIAwait(t, func() bool {
				live, err := sessionapi.OpenNative(ctx, client, "http://mekugi", main)
				if live != nil {
					live.Close()
				}
				var failure *sessionapi.NativeFailure
				return errors.As(err, &failure) && failure.Status == http.StatusNotFound
			})
			if mode == "stop" {
				return
			}
			saved, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{Workspace: workspace, Args: []string{"resume", main}})
			if err != nil {
				t.Fatal("resume did not reopen saved history:", err)
			}
			restored := view(saved)
			restored.await("Ready")
			if p.delegate {
				restored.await("Main finished before the exit choice.")
			} else {
				restored.await("Retained answer from background work.")
			}
			waitForTerminalExit(t, restored)
			requireTerminalOwnerExit(t, ctx, owners)
		})
	}
}

func requireTerminalOwnerExit(t *testing.T, ctx context.Context, owners <-chan error) {
	t.Helper()
	select {
	case err := <-owners:
		if err != nil {
			t.Fatal("owner did not close cleanly:", err)
		}
	case <-ctx.Done():
		t.Fatal("owner did not retire:", ctx.Err())
	}
}

func waitForTerminalExit(t *testing.T, terminal *appResumeTerminal) {
	t.Helper()
	terminal.send("\x03")
	timeout := time.NewTimer(8 * time.Second)
	defer timeout.Stop()
	frames := terminal.frames
	for {
		select {
		case frame, ok := <-frames:
			if ok {
				terminal.screen.Write(frame)
				if screen := terminal.screen.String(); strings.Contains(screen, "Exit this session?") {
					t.Fatalf("idle keyboard exit prompted for a running-work choice\n%s", screen)
				}
			} else {
				frames = nil
			}
		case err := <-terminal.done:
			if err != nil {
				t.Fatal(err)
			}
			terminal.checkRestored()
			return
		case <-timeout.C:
			t.Fatalf("idle keyboard exit did not close the terminal\n%s", terminal.screen.String())
		}
	}
}

func newTerminalExitTestServer(t *testing.T, ctx context.Context, codex, provider string, integration appServerIntegration) (*http.Client, string, <-chan error, func(*sessionapi.NativeConnection) *appResumeTerminal) {
	t.Helper()
	env, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	history := testHistory()
	review := new(changecapture.Service)
	views := testTerminalViews(review)
	serverContext, stop := context.WithCancel(ctx)
	owners := make(chan error, 4)
	launcher := &nativeReattachLauncher{
		prepare: func(_ context.Context, _ string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
			return sessionapi.NewPreparedInput(*input.NativeDraft, nil), nil
		},
	}
	hub := sessionapi.NewHub(serverContext, launcher)
	launcher.run = func(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) (runErr error) {
		defer func() { owners <- runErr }()
		_, resume, _, err := appServerArgs(start.Native.Args)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, codex, "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(provider+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2.disable_native_tools=false")
		cmd.Env, cmd.Dir = env, workspace
		conversation := new(conversationmodel.Service)
		observations := conversationbinding.Codex.Observation(conversation)
		session := &nativeSession{ctx: ctx, reviewSource: review, presentationState: testPresentation(), conversationState: conversation,
			activity: new(activitymodel.Service), history: history, segments: testReports(), observations: &observations, changeSource: integration.changeSource,
			preparer: integration.preparer, controlObservations: integration.controlObservations, audience: integration.audience, execWindows: integration.execWindows,
			execTrack: integration.execTrack, nativeTrace: integration.nativeTrace, retained: integration.retained}
		integration.bindForks(api)
		wait, err := session.start(testPreferences(), cmd, false, resume, nil, api, false, &threadExecution{hub: hub, setup: &threadSetup{}})
		if err != nil {
			return err
		}
		return wait()
	}
	dir, err := os.MkdirTemp("", "mekugi-exit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := sessionapi.ListenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serveSessionHub(serverContext, listener, hub) }()
	t.Cleanup(func() { stop(); <-done })
	transport := sessionapi.ControlTransport(socket)
	t.Cleanup(transport.CloseIdleConnections)
	view := func(connection *sessionapi.NativeConnection) *appResumeTerminal {
		t.Helper()
		ctx, cancel := context.WithCancel(ctx)
		outer, inner, err := pty.Open()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); outer.Close(); inner.Close() })
		if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
			t.Fatal(err)
		}
		state, err := term.GetState(int(inner.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		renderer, _ := views.NewAttachment(diffrender.EnvironmentTheme(""))
		terminal := &nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: renderer, views: views}, editors: make(map[string]*composer.Editor)}
		return observeAppResumeTerminal(t, ctx, cancel, outer, inner, state, func() error {
			_, err := terminal.run(ctx, inner, inner)
			return err
		})
	}
	return &http.Client{Transport: transport}, workspace, owners, view
}
