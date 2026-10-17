//go:build codex_e2e

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

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"
)

// The installed host owns name persistence; both model requests terminate at
// a local fixture. A fresh host resume proves the title is not only UI memory.
func TestAppServerSessionTitleNativeCodexE2E(t *testing.T) {
	testAppServerSessionTitleNativeCodexE2E(t, false)
}

func TestAppServerSessionTitleManualNativeCodexE2E(t *testing.T) {
	testAppServerSessionTitleNativeCodexE2E(t, true)
}

func testAppServerSessionTitleNativeCodexE2E(t *testing.T, manual bool) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	title := "Improve session naming"
	if manual {
		title = "My manually renamed session"
	}
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
		request, err := codexresponses.ParseRequest(body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if request.Model() == sessionTitleModel {
			if manual {
				namingCalls.Add(1)
				t.Error("manual rename made an automatic naming request")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, generatedTitleResponse)
				return
			}
			if namingCalls.Add(1) != 1 {
				t.Error("duplicate naming request")
				w.WriteHeader(400)
				return
			}
			if request.ReasoningEffort() != "medium" || binding.SubmittedUserText(request.Fields["input"], "/root", true) != "Explain native session naming" {
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
	provider := newTestProviderClient(upstream.URL, upstream.Client())
	generator := newTestSessionNaming(ctx, provider)
	handler := responsesHandler(ctx, time.Minute, requestexecution.Executor{Provider: provider, Client: provider, Naming: generator})
	threads := make(chan string, 8)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fixture authentication is added only at the local router boundary.
		r.Header.Set("Authorization", "Bearer fixture-token")
		r.Header.Set(openai.AccountIDHeader, "fixture-account")
		select {
		case threads <- r.Header.Get(codexresponses.ThreadIDHeader):
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
	start := func(resume string, naming *sessionNaming) (func(string, func(string) bool), func(string), func()) {
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
		conversation := new(conversationmodel.Service)
		observations := conversationbinding.Codex.Observation(conversation)
		reviewSource := new(changecapture.Service)
		wait, err := startAppServerUI(testTerminalViews(reviewSource), reviewSource, testPresentation(), conversation, new(activitymodel.Service), testHistory(), testPreferences(), testReports(), runCtx, command(runCtx), inner, inner, nil, nil, resume, true, nil, nil, nil, "", naming, &observations, false)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		waiterDone := make(chan struct{})
		go func() { defer close(waiterDone); done <- wait() }()
		screen := vt.NewEmulator(160, 30)
		stopReplies := startEmulatorReplies(outer, screen)
		chunks := readPTYChunks(runCtx, outer, 65536, 128)
		t.Cleanup(func() {
			stop()
			outer.Close()
			inner.Close()
			<-waiterDone
			for range chunks {
			}
			stopReplies()
		})
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
	if manual {
		send("/title " + title + "\r")
		await("manual Main title before first prompt", headerHasTitle)
		if primaryCalls.Load() != 0 || namingCalls.Load() != 0 {
			t.Fatal("manual title command reached the model")
		}
	}
	send("Explain native session naming\r")
	await("primary completion while naming gated", func(frame string) bool {
		return strings.Contains(frame, "Recovered after a retry.") && strings.Contains(frame, "╭─ Completed")
	})
	if !manual {
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
	}
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
	// A fresh generator and host process restore only durable title metadata.
	fresh := newTestSessionNaming(ctx, provider)
	handler = responsesHandler(ctx, time.Minute, requestexecution.Executor{Provider: provider, Client: provider, Naming: fresh})
	awaitResume, sendResume, quitResume := start(thread, fresh)
	awaitResume("persisted title after fresh app-server resume", func(frame string) bool { return headerHasTitle(frame) && strings.Contains(frame, "Ready") })
	if fresh.acceptCandidate(thread) {
		t.Fatal("fresh process remained eligible for title generation")
	}
	if manual {
		sendResume("Continue after manual rename\r")
		awaitResume("resumed completion", func(frame string) bool {
			return strings.Count(frame, "Recovered after a retry.") == 2 && strings.Contains(frame, "╭─ Completed")
		})
	}
	quitResume()
	wantPrimary, wantNaming := int32(1), int32(1)
	if manual {
		wantPrimary, wantNaming = 2, 0
	}
	if primaryCalls.Load() != wantPrimary || namingCalls.Load() != wantNaming {
		t.Fatalf("provider calls: primary=%d naming=%d", primaryCalls.Load(), namingCalls.Load())
	}
}
