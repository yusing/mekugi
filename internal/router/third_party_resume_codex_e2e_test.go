//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	vt "github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// Reuse the terminal observer and interactions, but pass the original launcher
// argv separately from the generated app-server startup configuration.
func startThirdPartyResumeTerminal(t *testing.T, newCommand func(context.Context) *exec.Cmd, thread string, argv []string) *appResumeTerminal {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	outer, inner, err := pty.Open()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); outer.Close(); inner.Close() })
	if err := pty.Setsize(outer, &pty.Winsize{Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(inner.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	wait, err := startAppServerUI(ctx, newCommand(ctx), inner, inner, nil, nil, thread, true, nil, nil, argv, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &appResumeTerminal{t: t, ctx: ctx, cancel: cancel, outer: outer, inner: inner,
		frames: make(chan []byte, 32), done: make(chan error, 1), screen: vt.NewEmulator(100, 30), before: before}
	t.Cleanup(func() { s.screen.Close() })
	go func() { _, _ = io.Copy(outer, s.screen) }()
	go func() { s.done <- wait() }()
	go func() {
		defer close(s.frames)
		buf := make([]byte, 65536)
		for {
			n, err := outer.Read(buf)
			if n > 0 {
				select {
				case s.frames <- bytes.Clone(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func TestThirdPartyResumeModelNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, unprefixed := range []bool{true, false} {
		mode := "standalone"
		prefix := "grok:"
		if unprefixed {
			mode, prefix = "dedicated", ""
		}
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			environment := routerFaultCodexEnvironment(t)
			filtered := make([]string, 0, len(environment))
			for _, entry := range environment {
				key, _, _ := strings.Cut(entry, "=")
				if key != "OPENAI_API_KEY" && key != "CODEX_API_KEY" && key != "CHATGPT_API_KEY" {
					filtered = append(filtered, entry)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			catalogCommand := exec.CommandContext(ctx, codex, "debug", "models", "--bundled")
			catalogCommand.Env = filtered
			bundled, err := catalogCommand.Output()
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := ProviderModelCatalog(bundled, Session{GrokEnabled: true, GrokUnprefixed: unprefixed, ThirdPartyOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			catalogPath := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(catalogPath, catalog, 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var models, threads []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertThirdPartyHeaderIsolation(t, r.Header, "resume-fixture-only")
				var wire map[string]any
				if err := json.UnmarshalRead(r.Body, &wire); err != nil {
					t.Error(err)
				}
				model, _ := wire["model"].(string)
				mu.Lock()
				models = append(models, model)
				index := len(models)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTestSSE(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": fmt.Sprintf("RESUME_OK_%d", index)}, "finish_reason": "stop"}}}))
			}))
			defer upstream.Close()
			provider := newProviderClient("http://unused.invalid", nil)
			provider.thirdPartyOnly = true
			provider.grok = &grokClient{httpClient: grokTestHTTPClient(t, upstream), auth: newGrokAuth("", "resume-fixture-only"), unprefixed: unprefixed}
			handler := responsesHandler(t.Context(), time.Minute, provider, nil, nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get(chatGPTAccountIDHeader) != "" {
					t.Error("third-party resume attached Codex authentication")
				}
				mu.Lock()
				threads = append(threads, r.Header.Get(threadIDHeader))
				mu.Unlock()
				handler(w, r)
			}))
			defer server.Close()
			workspace := t.TempDir()
			startupModel := prefix + "grok-4.7"
			newCommand := func(ctx context.Context) *exec.Cmd {
				command := exec.CommandContext(ctx, codex, "app-server",
					"-c", `model_providers.resume_fixture={name="resume_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false,supports_websockets=false}`,
					"-c", `model_provider="resume_fixture"`, "-c", "model="+strconv.Quote(startupModel),
					"-c", "model_catalog_json="+strconv.Quote(catalogPath), "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
				command.Env, command.Dir = filtered, workspace
				return command
			}
			argv := []string{"mekugi"}
			if unprefixed {
				argv = append(argv, "grok")
			}
			first := startThirdPartyResumeTerminal(t, newCommand, "", argv)
			first.await("Ready")
			first.send("/model " + prefix + "grok-4.5\r")
			first.await(prefix + "grok-4.5")
			first.send("Saved selection\r")
			first.await("RESUME_OK_1")
			first.await("completed")
			first.quit()
			mu.Lock()
			thread := threads[0]
			mu.Unlock()
			if thread == "" {
				t.Fatal("saved inference omitted thread identity")
			}
			resumeArgv := append(append([]string(nil), argv...), "resume", thread)
			second := startThirdPartyResumeTerminal(t, newCommand, thread, resumeArgv)
			second.await("Ready")
			second.send("Restore saved model rather than automatic default\r")
			second.await("RESUME_OK_2")
			second.await("completed")
			second.quit()
			third := startThirdPartyResumeTerminal(t, newCommand, "", argv)
			third.await("Ready")
			third.send("Fresh default\r")
			third.await("RESUME_OK_3")
			third.await("completed")
			third.send("/resume\r")
			third.await("Resume a previous session")
			third.awaitMatch("saved and current sessions", func(screen string) bool {
				return strings.Contains(screen, "Saved selection") && strings.Contains(screen, "Fresh default · current")
			})
			third.send("\x1b[B\r")
			third.awaitMatch("switched to saved transcript", func(screen string) bool {
				return strings.Contains(screen, "Saved selection") && !strings.Contains(screen, "Fresh default") && !strings.Contains(screen, "Resume a previous session")
			})
			third.send("Session switch keeps saved model\r")
			third.await("RESUME_OK_4")
			third.await("completed")
			third.quit()
			startupModel = prefix + "grok-4.6"
			explicitArgv := append(append([]string(nil), resumeArgv...), "-m", startupModel)
			fourth := startThirdPartyResumeTerminal(t, newCommand, thread, explicitArgv)
			fourth.await("Ready")
			fourth.send("Explicit model overrides saved selection\r")
			fourth.await("RESUME_OK_5")
			fourth.await("completed")
			fourth.quit()
			mu.Lock()
			defer mu.Unlock()
			want := []string{"grok-4.5", "grok-4.5", "grok-4.7", "grok-4.5", "grok-4.6"}
			if !slices.Equal(models, want) {
				t.Fatalf("actual inference models=%q, want %q", models, want)
			}
			if len(threads) != 5 || threads[1] != thread || threads[2] == thread || threads[3] != thread || threads[4] != thread {
				t.Fatalf("resume/session-switch thread identity mismatch: %q", threads)
			}
		})
	}
}
