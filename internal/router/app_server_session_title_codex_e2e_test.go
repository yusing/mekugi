//go:build journal_e2e

package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// The installed host owns name persistence; both model requests terminate at
// a local fixture. A fresh host resume proves the title is not only UI memory.
func TestAppServerSessionTitleNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	const title = "Improve session naming"
	namingStarted, releaseNaming := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseNaming) }) }
	var primaryCalls, namingCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		request, err := parseResponsesRequest(body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if request.model() == sessionTitleModel {
			if namingCalls.Add(1) != 1 {
				t.Error("duplicate naming request")
				w.WriteHeader(400)
				return
			}
			if request.reasoningEffort() != "medium" || journalQuestionFromInput(request.fields["input"], "/root") != "Explain native session naming" {
				t.Error("naming request lost medium effort or first prompt")
			}
			close(namingStarted)
			select {
			case <-releaseNaming:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, generatedTitleResponse)
			return
		}
		primaryCalls.Add(1)
		response := routerFaultCodexSuccessResponse()
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		_, _ = io.Copy(w, response.Body)
	}))
	defer upstream.Close()
	// Unblock a gated upstream before httptest.Server.Close on any failure.
	defer release()
	provider := newProviderClient(upstream.URL, upstream.Client())
	generator := newSessionTitleGenerator(ctx, provider, newSessionTitleCacheAt(""))
	provider.titleGenerator = generator
	handler := responsesHandler(ctx, time.Minute, provider, nil, nil)
	threads := make(chan string, 8)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fixture authentication is added only at the local router boundary.
		r.Header.Set("Authorization", "Bearer fixture-token")
		r.Header.Set(chatGPTAccountIDHeader, "fixture-account")
		select {
		case threads <- r.Header.Get(threadIDHeader):
		default:
		}
		handler.ServeHTTP(w, r)
	}))
	defer router.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	command := func(runCtx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(runCtx, codex, "app-server", "-c", `model_providers.titlefixture={name="preview",base_url=`+strconv.Quote(router.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false,supports_websockets=false}`, "-c", `model_provider="titlefixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	start := func(resume string, naming *sessionTitleGenerator) (func(string, func(string) bool), func(string), func()) {
		t.Helper()
		runCtx, stop := context.WithCancel(ctx)
		outer, inner, err := pty.Open()
		if err != nil {
			stop()
			t.Fatal(err)
		}
		t.Cleanup(func() { stop(); outer.Close(); inner.Close() })
		if err := pty.Setsize(outer, &pty.Winsize{Cols: 160, Rows: 30}); err != nil {
			t.Fatal(err)
		}
		wait, err := startAppServerUI(runCtx, command(runCtx), inner, inner, nil, nil, resume, true, nil, nil, nil, "", naming)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- wait() }()
		screen := vt.NewEmulator(160, 30)
		// Emulator.Close is not synchronized with its reply reader. Wake that
		// reader with a terminal-status response and join it before closing.
		var stoppingReplies atomic.Bool
		repliesDone := make(chan struct{})
		go func() {
			defer close(repliesDone)
			buf := make([]byte, 4096)
			for {
				n, err := screen.Read(buf)
				if err != nil {
					return
				}
				if stoppingReplies.Load() && string(buf[:n]) == "\x1b[?0n" {
					return
				}
				// PTY shutdown can precede this cleanup handshake. Keep draining
				// emulator replies even when the terminal no longer accepts them.
				_, _ = outer.Write(buf[:n])
			}
		}()
		t.Cleanup(func() {
			stoppingReplies.Store(true)
			_, _ = screen.Write([]byte("\x1b[5n"))
			<-repliesDone
			_ = screen.Close()
		})
		chunks := readPTYChunks(runCtx, outer, 65536, 128)
		await := func(label string, predicate func(string) bool) {
			t.Helper()
			for !predicate(screen.String()) {
				select {
				case chunk, ok := <-chunks:
					if !ok {
						t.Fatalf("terminal closed before %s:\n%s", label, screen.String())
					}
					if _, err := screen.Write(chunk); err != nil {
						t.Fatal(err)
					}
				case err := <-done:
					t.Fatalf("UI exited before %s: %v\n%s", label, err, screen.String())
				case <-runCtx.Done():
					t.Fatalf("timeout before %s: %v\n%s", label, runCtx.Err(), screen.String())
				}
			}
		}
		send := func(input string) {
			t.Helper()
			if _, err := io.WriteString(outer, input); err != nil {
				t.Fatal(err)
			}
		}
		quit := func() {
			t.Helper()
			send("/quit\r")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-runCtx.Done():
				t.Fatal(runCtx.Err())
			}
			stop()
			outer.Close()
			inner.Close()
		}
		return await, send, quit
	}
	headerHasTitle := func(frame string) bool {
		return strings.Contains(strings.Split(frame, "\n")[0], title)
	}
	await, send, quit := start("", generator)
	await("startup", func(frame string) bool { return strings.Contains(frame, "Ready") })
	send("Explain native session naming\r")
	await("primary completion while naming gated", func(frame string) bool {
		return strings.Contains(frame, "Recovered after a retry.") && strings.Contains(frame, "╭─ Completed")
	})
	select {
	case <-namingStarted:
	case <-ctx.Done():
		t.Fatal("naming did not start", ctx.Err())
	}
	await("absence of unconfirmed title", func(frame string) bool {
		if headerHasTitle(frame) {
			t.Fatal("title displayed before naming response was released")
		}
		return true
	})
	var thread string
	select {
	case thread = <-threads:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if thread == "" {
		t.Fatal("primary request supplied no native thread identity")
	}
	release()
	await("host-confirmed Main title", headerHasTitle)
	quit()
	// No generator is supplied to the fresh native loop. Only host metadata can
	// restore this Main title, and resuming must not make another model request.
	awaitResume, _, quitResume := start(thread, nil)
	awaitResume("persisted title after fresh app-server resume", func(frame string) bool { return headerHasTitle(frame) && strings.Contains(frame, "Ready") })
	quitResume()
	if primaryCalls.Load() != 1 || namingCalls.Load() != 1 {
		t.Fatalf("provider calls: primary=%d naming=%d", primaryCalls.Load(), namingCalls.Load())
	}
}
